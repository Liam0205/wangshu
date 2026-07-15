// Arith IC dual-counter repurposing (`docs/design/p2-bridge/02-ic-feedback.md` §3).
//
// P1 write-only, no read (05 §6.4): the arithmetic fast path checks
// IsNumber(b) && IsNumber(c) inline and does not read the IC slot to pick a
// branch. The arithmetic IC is a pure side-channel write — the P2 aggregator
// reads these counts to compute confidence (numHits / (numHits+metaHits)).
//
// Field repurposing convention (02 §3.2 + bytecode/proto.go ICSlot comment):
//   - Shape    = numHits  (fast-path dual-number hit count)
//   - Index    = metaHits (slow-path string coercion / metamethod hit count)
//   - TableRef = left as constant 0 (arithmetic has no table, no identity)
//   - Kind     = 0 unobserved / 1 observed (P2 reads it as "already executed,
//     dual counters readable")
//
// **Saturate, no wrap-around** (02 §3.3 + invariant 6): numHits / metaHits stop
// incrementing as they approach 2^32, avoiding a sudden confidence shift after
// a super-hot spot runs 2^32 times. The saturation value 2^32-1 has a
// completely negligible effect on the `numHits/total` ratio.
package crescent

import (
	"github.com/Liam0205/wangshu/internal/bytecode"
)

const arithCounterSaturation uint32 = ^uint32(0) // 2^32-1, saturation cap

// recordArithNumHit records an arithmetic fast-path dual-number hit (02 §3.3).
//
//go:nosplit
func recordArithNumHit(s *bytecode.ICSlot) {
	if s.Shape != arithCounterSaturation {
		s.Shape++
	}
	s.Kind = 1 // observed (arithmetic IC Kind∈{0,1}; table IC kind∈{1,2,3,4})
}

// recordArithMetaHit records an arithmetic slow-path string coercion /
// metamethod hit (02 §3.3).
//
//go:nosplit
func recordArithMetaHit(s *bytecode.ICSlot) {
	if s.Index != arithCounterSaturation {
		s.Index++
	}
	s.Kind = 1
}
