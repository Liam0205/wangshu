//go:build wangshu_p4 && !(linux && amd64)

package amd64

// CallJITSpec is a non-linux/amd64 placeholder (only makes cross-compile pass —
// PJ2 speculation is not implemented on this platform, and Compile does not
// call this function).
func CallJITSpec(codeAddr uintptr, jitCtx uintptr, vsBase uintptr) uint64 {
	_ = codeAddr
	_ = jitCtx
	_ = vsBase
	panic("internal/gibbous/jit/amd64: CallJITSpec not supported on this OS/arch")
}
