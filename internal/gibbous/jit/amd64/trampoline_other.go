//go:build wangshu_p4 && !(linux && amd64)

package amd64

// CallJIT panics on non-linux/amd64 platforms — the PJ1 acceptance platform is
// linux/amd64 (06 §6.1, PJ1 row).
//
// When wangshu_p4 is enabled on a non-linux/amd64 platform, Compiler.SupportsAllOpcodes
// still returns false everywhere (per the PJ0 acceptance rule), so this function
// should never be reached; reaching it means the upstream bypassed the F7 gate.
func CallJIT(codeAddr uintptr) uint64 {
	_ = codeAddr
	panic("internal/gibbous/jit/amd64: CallJIT unsupported on this platform (PJ1 only on linux/amd64)")
}
