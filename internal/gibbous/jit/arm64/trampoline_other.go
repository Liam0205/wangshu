//go:build wangshu_p4 && !(arm64 && (linux || (darwin && cgo)))

package arm64

// CallJITFull is a placeholder stub for non-real platforms — used to make cross-compilation pass.
//
// Path distribution (complements trampoline_real.go build tags):
//   - linux/arm64                    → trampoline_real.go + trampoline_arm64.s
//   - darwin/arm64 && cgo            → trampoline_real.go + trampoline_arm64.s (same Plan 9 asm)
//   - darwin/arm64 && !cgo           → this stub (cross-build CGO_ENABLED=0)
//   - others (amd64 / windows / 32-bit) → this stub (cross-compile smoke test)
//
// The amd64 build uses arch_amd64.go and never actually calls the arm64 package's CallJITFull/CallJITSpec.
func CallJITFull(codeAddr uintptr, jitCtxAddr uintptr) uint64 {
	_ = codeAddr
	_ = jitCtxAddr
	panic("internal/gibbous/jit/arm64: CallJITFull not supported on this OS/arch (CGO_ENABLED=0 stub on darwin/arm64;amd64 应走 arch_amd64.go)")
}

// CallJITSpec is a placeholder stub for non-real platforms — same as CallJITFull, used to make cross-compilation pass.
func CallJITSpec(codeAddr uintptr, jitCtxAddr uintptr, vsBaseAddr uintptr) uint64 {
	_ = codeAddr
	_ = jitCtxAddr
	_ = vsBaseAddr
	panic("internal/gibbous/jit/arm64: CallJITSpec not supported on this OS/arch (CGO_ENABLED=0 stub on darwin/arm64;amd64 应走 arch_amd64.go)")
}
