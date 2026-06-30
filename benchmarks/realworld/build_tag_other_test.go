//go:build !wangshu_p3

package realworld

// isP3Build returns (false, false) outside wangshu_p3 build tag.
// Used to skip subtests known broken under P3 build (e.g.,
// binarytrees parity, see TODO(P3-binarytrees-parity)).
func isP3Build() (bool, bool) { return false, false }
