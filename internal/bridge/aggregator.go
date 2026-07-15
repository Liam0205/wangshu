// IC feedback aggregator (`docs/design/p2-bridge/02-ic-feedback.md` §6).
//
// Input: the Proto's ICSlot array (written by P1, side-channel fed);
// Output: PointFeedback aggregation product indexed by pc (attached to ProfileData.Feedback).
//
// **P2 writes without consuming** (02 §7): the aggregator is pure-read ICSlot +
// pure-write TypeFeedback, and reads no feedback field itself.
package bridge

import (
	"fmt"
	"sync/atomic"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// Aggregator configuration (02 §5 default values).
const (
	// StableArithThreshold is the arithmetic IC stability threshold: numHits/total ≥
	// this value to judge FBArithStableNumber (02 §5.2, default 0.99 is on the
	// conservative side; V8/SpiderMonkey empirical range [0.97, 0.99]).
	StableArithThreshold float32 = 0.99

	// MinObservations is the arithmetic IC sample-count lower bound: numHits+metaHits <
	// this value is considered statistically meaningless, judged FBUnstable directly
	// (02 §5.3, 100 gives a ±10% confidence interval).
	MinObservations uint64 = 100

	// MegamorphicRefillThreshold is the table IC refill-count threshold (P2 follow-up
	// optimization round #4, 02 §6.2 scheme (B) simplified version): once the same IC
	// slot has undergone more than N "miss-after-fill refills" (different target
	// table/shape ⟹ must drop the old slot and rebuild), it is marked megamorphic.
	// Default 3: the first fill (Kind=0→1/2) doesn't count, accumulation starts from
	// the 1st refill; 3 refills roughly correspond to "this point has accessed 4
	// different tables/shapes", which is already polymorphic statistically.
	MegamorphicRefillThreshold uint8 = 3
)

// Aggregator is the IC feedback aggregator (02 §6.4). Stateless, pure-function
// wrapper; a new one is created each time (not shared across Protos — this design
// currently has no internal state, the wrapper is for future extension).
type Aggregator struct {
	stableArithThreshold float32
	minObservations      uint64
	globalGen            atomic.Uint32 // generation counter (02 §4.1), allocated monotonically increasing
}

// NewAggregator constructs an aggregator with default thresholds.
func NewAggregator() *Aggregator {
	return &Aggregator{
		stableArithThreshold: StableArithThreshold,
		minObservations:      MinObservations,
	}
}

// Aggregate aggregates all IC observations of a Proto into one TypeFeedback (02 §6.4).
//
// Properties:
//   - O(N) single pass (N = len(Proto.Code));
//   - side-effect free — doesn't write ICSlot, doesn't write ProfileData
//     (installFeedback CASes separately);
//   - reentrant — concurrent calls from multiple goroutines on the same Proto are
//     safe (all read-only ICSlot, race-tolerant reads; 02 §5.4).
func (a *Aggregator) Aggregate(proto *bytecode.Proto) *TypeFeedback {
	fb := &TypeFeedback{
		Points:     make([]PointFeedback, len(proto.Code)),
		Generation: a.globalGen.Add(1),
	}
	for pc, ins := range proto.Code {
		op := bytecode.Op(ins)
		slot := &proto.IC[pc]
		switch op {
		case bytecode.ADD, bytecode.SUB, bytecode.MUL,
			bytecode.DIV, bytecode.MOD, bytecode.POW,
			bytecode.UNM, bytecode.LT, bytecode.LE:
			fb.Points[pc] = a.extractArithFeedback(int32(pc), slot)
		case bytecode.GETTABLE:
			fb.Points[pc] = a.extractTableFeedback(int32(pc), slot, opGetTable)
		case bytecode.SETTABLE:
			fb.Points[pc] = a.extractTableFeedback(int32(pc), slot, opSetTable)
		case bytecode.GETGLOBAL:
			fb.Points[pc] = a.extractTableFeedback(int32(pc), slot, opGetGlobal)
		case bytecode.SETGLOBAL:
			fb.Points[pc] = a.extractTableFeedback(int32(pc), slot, opSetGlobal)
		case bytecode.SELF:
			fb.Points[pc] = a.extractTableFeedback(int32(pc), slot, opSelf)
		default:
			// Non-IC instruction: fb.Points[pc] stays zero-valued (Kind=FBUnstable, Confidence=0)
			// — P3/P4 should skip this pc.
		}
	}
	return fb
}

// opTableKind identifies the opcode subclass of a table IC (02 §6.3).
type opTableKind uint8

const (
	opGetTable opTableKind = iota
	opSetTable
	opGetGlobal
	opSetGlobal
	opSelf
)

// extractArithFeedback aggregates arithmetic IC (02 §3.4).
//
// Input ICSlot:
//   - Shape    = numHits
//   - Index    = metaHits
//   - Kind     = 0 not observed / 1 observed
//
// Output PointFeedback:
//   - Kind = FBArithStableNumber (ratio ≥ 0.99 + total ≥ 100)
//     / FBUnstable (otherwise)
//   - Confidence = ratio (carried for diagnostics, even when Unstable)
func (a *Aggregator) extractArithFeedback(pc int32, slot *bytecode.ICSlot) PointFeedback {
	if slot.Kind == 0 {
		return PointFeedback{} // skip: not observed
	}
	// On an arithmetic IC, Kind must be 1 (the P2 aggregator only reads what P1
	// writes; any other value is a P1 write-contract violation). Note the
	// race-tolerant read: in a multi-State concurrent scenario P1 is still
	// writing, so reading "Kind=1 but Shape/Index 0" is a legal transient state (02 §5.4).
	if slot.Kind != 1 {
		panic(fmt.Sprintf("bridge: arith IC at pc=%d has kind=%d, expected 0 or 1",
			pc, slot.Kind))
	}
	numHits := atomic.LoadUint32(&slot.Shape) // 02 §5.4 race-tolerant read
	metaHits := atomic.LoadUint32(&slot.Index)
	total := uint64(numHits) + uint64(metaHits)
	if total < a.minObservations {
		return PointFeedback{
			PC:           pc,
			Kind:         FBUnstable,
			Confidence:   0.0, // explicit 0, marks "insufficient sample"
			Observations: uint32(total),
		}
	}
	ratio := float32(numHits) / float32(total)
	pf := PointFeedback{
		PC:           pc,
		Confidence:   ratio,
		Observations: uint32(total),
	}
	if ratio >= a.stableArithThreshold {
		pf.Kind = FBArithStableNumber
	} else {
		pf.Kind = FBUnstable
	}
	return pf
}

// extractTableFeedback aggregates table / global / SELF IC (02 §6.3).
func (a *Aggregator) extractTableFeedback(pc int32, slot *bytecode.ICSlot, opType opTableKind) PointFeedback {
	if slot.Kind == 0 {
		return PointFeedback{} // skip: not observed
	}
	pf := PointFeedback{
		PC:           pc,
		Confidence:   1.0, // 02 §5.1: table IC mono is 1.0
		StableShape:  slot.Shape,
		StableIndex:  slot.Index,
		Observations: 1, // 02 §5.1: table IC not counted by P1, fill placeholder 1
	}
	// P2+ #4 megamorphic active detection: once Refill count exceeds the threshold ⟹
	// this point is polymorphic, actively translate to FBTableMega (overriding the
	// original mono kind judgment). This makes up for the gap that P1 currently
	// doesn't actively write ICKindMegamorphic (02 §6.2 scheme (A) → (B) upgrade).
	if slot.Refill >= MegamorphicRefillThreshold {
		pf.Kind = FBTableMega
		pf.Confidence = 0.0
		pf.StableShape = 0
		pf.StableIndex = 0
		return pf
	}
	switch slot.Kind {
	case bytecode.ICKindArrayHit, bytecode.ICKindNodeHit, bytecode.ICKindMonoMeta:
		switch opType {
		case opGetTable, opSetTable:
			pf.Kind = FBTableMono
		case opGetGlobal, opSetGlobal:
			pf.Kind = FBGlobalStable
		case opSelf:
			pf.Kind = FBSelfMono
		}
	case bytecode.ICKindMegamorphic:
		pf.Kind = FBTableMega
		pf.Confidence = 0.0
		pf.StableShape = 0
		pf.StableIndex = 0
	default:
		panic(fmt.Sprintf("bridge: table IC at pc=%d has unexpected kind=%d",
			pc, slot.Kind))
	}
	return pf
}

// installFeedback attaches the aggregation product to ProfileData on the Bridge
// side (only once per State, 02 §4.5 + §5.5 no-reaggregation policy). The current
// implementation is a non-atomic write — profileTable is State-private, no
// concurrent contention (01 §6.3 scheme (B)).
//
// Concurrent feedback writes on a Proto shared across States are a separate
// dimension (Proto side-aggregation table, 02 §5.5 CAS install) — it doesn't
// exist under P2 PB0/PB1/PB2's scheme (B), the CAS will be added when scheme (C)
// is enabled. Currently ProfileData's Feedback field is State-private, a plain
// assignment suffices.
func (b *Bridge) installFeedback(proto *bytecode.Proto, fb *TypeFeedback) {
	pd := b.profileOf(proto)
	if pd.Feedback == nil {
		pd.Feedback = fb
	}
	// don't overwrite existing feedback (first version aggregates only once)
}
