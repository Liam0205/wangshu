// TypeFeedback — the aggregated product of IC feedback (`docs/design/p2-bridge/02-ic-feedback.md` §4).
//
// **Design core**: P2 writes feedback but does not consume it (02 §7); P3/P4 read and
// consume it, optional for P3 and central for P4.
package bridge

// FeedbackKind describes the stable shape observed by the IC at a given pc (02 §4.3).
//
// **Asymmetric consumption** (02 §1.4 + 05 §1):
//   - P3 (try-compile): uses kind to decide "which fast path to inline, which IC
//     snapshot to freeze"; on failure the slow path is still correct. The confidence
//     field is a hint that P3 may ignore.
//   - P4 (speculative JIT): uses both kind and confidence; only confidence ≥ 0.99
//     emits a speculative template; a failed guard ⇒ deopt back to the interpreter.
//
// **Zero feedback does not affect correctness**: a point with kind=FBUnstable /
// confidence=0 → both P3/P4 fall back to generic translation (losing speedup but
// still correct).
type FeedbackKind uint8

const (
	// FBUnstable — the "do not speculate" marker. Merges three sources:
	//   1. The point was never observed by the IC (ICSlot.kind=0)
	//   2. The arithmetic-point ratio is below threshold (<0.99) or the sample
	//      count is insufficient (<minObservations)
	//   3. Default fill for non-IC points (LOADK / MOVE / RETURN, etc.)
	FBUnstable FeedbackKind = iota

	// FBArithStableNumber — arithmetic point with consistently number operands
	// (≥99% numHits/total). P4 emits an f64 fast path + guard accordingly; a
	// failed guard deopts (P4 §IC speculation).
	FBArithStableNumber

	// FBTableMono — table access is monomorphic and stable (GETTABLE/SETTABLE,
	// kind∈{1,2,3}). P4 speculates a direct slot access: guard "target table
	// gen == stableShape" + direct index.
	FBTableMono

	// FBTableMega — table access is megamorphic (02 §6.3 kind=4 defensive translation).
	// An explicit "do not speculate" marker — at this point P4 should take the
	// generic hash lookup path.
	FBTableMega

	// FBGlobalStable — global read is constant. For GETGLOBAL/SETGLOBAL the globals
	// are a single table, so a node hit means stable; P4/P3 may constant-fold the
	// stableIndex slot.
	FBGlobalStable

	// FBSelfMono — method call is monomorphic (SELF + the method dispatch point of
	// the immediately following CALL). P4 inlines the method lookup accordingly:
	// guard metatable gen + direct method slot.
	FBSelfMono
)

func (k FeedbackKind) String() string {
	switch k {
	case FBArithStableNumber:
		return "ArithStableNumber"
	case FBTableMono:
		return "TableMono"
	case FBTableMega:
		return "TableMega"
	case FBGlobalStable:
		return "GlobalStable"
	case FBSelfMono:
		return "SelfMono"
	default:
		return "Unstable"
	}
}

// PointFeedback is the feedback snapshot at a single pc (02 §4.2).
type PointFeedback struct {
	// PC is the index of this point in Proto.Code (a redundant field, equal to the
	// TypeFeedback.Points index). Kept so a standalone point does not lose its position.
	PC int32

	// Kind is the feedback type of this point.
	Kind FeedbackKind

	// Confidence ∈ [0.0, 1.0]. Its meaning varies with Kind (02 §5.1):
	//   - FBArithStableNumber: numHits/(numHits+metaHits) (the true ratio)
	//   - FBTableMono / FBGlobalStable / FBSelfMono: 1.0 (P1 mono IC does not degrade)
	//   - FBTableMega: 0.0 (explicit "do not speculate" marker)
	//   - FBUnstable: 0.0 or a diagnostic ratio
	Confidence float32

	// StableShape: the "stable shape" of a table/global point — a snapshot of
	// ICSlot.shape (the target table's gen, i.e. generation number). When P4
	// speculates a direct slot access it guards "current table gen == stableShape",
	// deopting on failure. Not filled for arithmetic points (0).
	StableShape uint32

	// StableIndex: the "stable slot index" of a table/global point — a snapshot of
	// ICSlot.index. On a P4 hit this slot is indexed directly, without a hash lookup.
	// Not filled for arithmetic points (0).
	StableIndex uint32

	// Observations: the observation count accumulated during aggregation (arithmetic
	// point = numHits+metaHits; table point = hit count, which the current P1
	// implementation does not count separately, so a placeholder 1 is filled by
	// default). Downstream may set a minimum sample-count threshold (e.g. P3 "does
	// not emit a compact translation for points with <100 observations").
	Observations uint32
}

// TypeFeedback is the aggregated product of all IC observations for one Proto (02 §4.1).
//
// It indexes the type-stability judgment of each program point by pc. **P2 produces
// but does not use it itself** (02 §7).
type TypeFeedback struct {
	// Points is indexed by pc; its length = len(Proto.Code). A slot for a non-IC
	// point has Kind=FBUnstable, Confidence=0, and P3/P4 should skip it.
	Points []PointFeedback

	// Generation is the generation number of the feedback snapshot, incremented each
	// time P2 re-aggregates. If the snapshot P3/P4 receive has a Generation lagging
	// behind the current one, it means P1 wrote another batch of observations during
	// aggregation — but this does not affect correctness (P3 generic translation
	// always provides a fallback, and a P4 guard always catches the actual runtime
	// deviation).
	Generation uint32
}
