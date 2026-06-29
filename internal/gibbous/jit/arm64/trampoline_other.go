//go:build wangshu_p4 && !(arm64 && (linux || (darwin && cgo)))

package arm64

// CallJITFull 非真实装平台占位 stub——用于 cross-compile 通过。
//
// 路径分布(承 trampoline_real.go build tag 互补):
//   - linux/arm64                    → trampoline_real.go + trampoline_arm64.s
//   - darwin/arm64 && cgo            → trampoline_real.go + trampoline_arm64.s (同份 Plan 9 asm)
//   - darwin/arm64 && !cgo           → 本 stub (cross-build CGO_ENABLED=0)
//   - 其它 (amd64 / windows / 32-bit) → 本 stub (cross-compile 冒烟)
//
// amd64 build 用 arch_amd64.go,不会真调到 arm64 包的 CallJITFull/CallJITSpec。
func CallJITFull(codeAddr uintptr, jitCtxAddr uintptr) uint64 {
	_ = codeAddr
	_ = jitCtxAddr
	panic("internal/gibbous/jit/arm64: CallJITFull not supported on this OS/arch (CGO_ENABLED=0 stub on darwin/arm64;amd64 应走 arch_amd64.go)")
}

// CallJITSpec 非真实装平台占位 stub——同 CallJITFull,用于 cross-compile 通过。
func CallJITSpec(codeAddr uintptr, jitCtxAddr uintptr, vsBaseAddr uintptr) uint64 {
	_ = codeAddr
	_ = jitCtxAddr
	_ = vsBaseAddr
	panic("internal/gibbous/jit/arm64: CallJITSpec not supported on this OS/arch (CGO_ENABLED=0 stub on darwin/arm64;amd64 应走 arch_amd64.go)")
}
