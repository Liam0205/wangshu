//go:build wangshu_p4 && !(linux && amd64)

package amd64

// CallJITSpec 非 linux/amd64 占位(仅 cross-compile 通过——本平台 PJ2
// 投机不实装,Compile 不调本函数)。
func CallJITSpec(codeAddr uintptr, jitCtx uintptr, vsBase uintptr) uint64 {
	_ = codeAddr
	_ = jitCtx
	_ = vsBase
	panic("internal/gibbous/jit/amd64: CallJITSpec not supported on this OS/arch")
}
