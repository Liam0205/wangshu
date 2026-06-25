//go:build wangshu_p4 && !(linux && amd64)

package amd64

// CallJITFull 在非 linux/amd64 平台上 panic(承 PJ1 范围裁决:验收平台 = linux/amd64)。
func CallJITFull(codeAddr uintptr, jitCtx uintptr) uint64 {
	_ = codeAddr
	_ = jitCtx
	panic("internal/gibbous/jit/amd64: CallJITFull unsupported on this platform")
}
