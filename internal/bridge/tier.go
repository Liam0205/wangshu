// TierState — single-direction absorbing state machine
// (`docs/design/p2-bridge/04-try-compile-fallback.md` §2).
package bridge

// TierState is the tier up/down state machine (attached to ProfileData.TierState).
//
// Three-state enum:
//   - TierInterp   start: every Proto begins here; no tier-up has happened
//   - TierGibbous  absorbing state of a successful tier-up: the Proto is
//     compiled, the P3 trampoline takes over
//   - TierStuck    absorbing state of permanent interpretation: not compilable /
//     compilation failed / backend unsupported
//
// **Invariant** (04 §2.4 formal argument): the state machine is one-directional
// + absorbing, with **no** TierGibbous→TierInterp / TierStuck→TierInterp reverse
// edge — this is the literal embodiment of "zero deopt": in the P2 phase a Proto
// never "tiers up and then comes back" (back edges are only introduced by P4).
//
// Numeric convention: TierInterp = 0 (Go zero value). `pd := &ProfileData{}` is
// thus equivalent to `pd.TierState == TierInterp`, consistent with the lazy
// building of profileTable.
type TierState uint8

const (
	TierInterp  TierState = iota // 0: interpreting (default start)
	TierGibbous                  // 1: tiered up to gibbous (shared landing for P3/P4)
	TierStuck                    // 2: permanent interpretation (not compilable / compilation failed)
)

func (t TierState) String() string {
	switch t {
	case TierInterp:
		return "interp"
	case TierGibbous:
		return "gibbous"
	case TierStuck:
		return "stuck"
	default:
		return "unknown"
	}
}
