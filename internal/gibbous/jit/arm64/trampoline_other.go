//go:build wangshu_p4 && !(linux && arm64)

package arm64

// CallJITFull 非 linux/arm64 平台占位 stub——用于 cross-compile 通过(amd64
// build 用 arch_amd64.go,不会真调到 arm64 包的 CallJITFull;darwin/arm64
// 完整支持留 PJ8+ MAP_JIT spike 同批落地)。
func CallJITFull(codeAddr uintptr, jitCtxAddr uintptr) uint64 {
	_ = codeAddr
	_ = jitCtxAddr
	panic("internal/gibbous/jit/arm64: CallJITFull not supported on this OS/arch")
}
