//go:build wangshu_p4 && !(linux && amd64)

package amd64

// CallJITFull panics on non-linux/amd64 platforms (per the PJ1 scope ruling:
// acceptance platform = linux/amd64).
func CallJITFull(codeAddr uintptr, jitCtx uintptr) uint64 {
	_ = codeAddr
	_ = jitCtx
	panic("internal/gibbous/jit/amd64: CallJITFull unsupported on this platform")
}
