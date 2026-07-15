//go:build wangshu_p4 && !(linux && arm64)

package arm64

// flushICacheArm64Asm is a placeholder stub for non-linux/arm64 (the amd64
// build / other-arch builds never actually instantiate jitarm64's execution
// path; this only exists so cross-compilation passes).
func flushICacheArm64Asm(start, end uintptr) {
	_ = start
	_ = end
}
