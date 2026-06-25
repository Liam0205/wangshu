//go:build wangshu_p4 && !(linux && arm64)

package arm64

// flushICacheArm64Asm 非 linux/arm64 占位 stub(amd64 build / 其它 arch
// build 不真实例化 jitarm64 的执行路径,仅 cross-compile 通过)。
func flushICacheArm64Asm(start, end uintptr) {
	_ = start
	_ = end
}
