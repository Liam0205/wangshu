//go:build wangshu_p4 && !(linux && amd64)

package amd64

// CallJIT 在非 linux/amd64 平台上 panic——PJ1 阶段验收平台 = linux/amd64
// (06 §6.1 PJ1 行)。
//
// 非 linux/amd64 平台启用 wangshu_p4 时,Compiler.SupportsAllOpcodes 仍全
// false(承 PJ0 验收口径),不应触达本函数;若触达说明上游绕过了 F7 闸门。
func CallJIT(codeAddr uintptr) uint64 {
	_ = codeAddr
	panic("internal/gibbous/jit/amd64: CallJIT unsupported on this platform (PJ1 only on linux/amd64)")
}
