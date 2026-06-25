//go:build wangshu_p4

package jit

import "unsafe"

// jitContextAddr 把 *JITContext 转 uintptr,供 callJITFull 的 r15 装载入参。
//
// **unsafe 范围**:JITContext 是 Go 堆对象,GC 不会移动 Go 堆;uintptr 在
// JITContext 生命期内稳定(承 05-system-pipeline §1.3.4 「JIT 不持任何 Go 栈
// 指针,jitContext 在 Go 堆」)。
//
// 注:本函数返回 uintptr 后,调用方负责在 JIT 世界期间不让 jitCtx 被 GC
// 回收(p4Code 持 *JITContext 字段保活)。
func jitContextAddr(ctx *JITContext) uintptr {
	if ctx == nil {
		return 0
	}
	return uintptr(unsafe.Pointer(ctx))
}
