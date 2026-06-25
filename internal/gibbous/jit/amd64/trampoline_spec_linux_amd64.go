//go:build wangshu_p4 && linux && amd64

package amd64

// callJITSpec 是 P4 PJ2 投机模板专用 trampoline:除装 r15=jitContext 外,
// 也装 rbx=valueStackBase 让段内字节级 codegen(EmitArithSpeculativeAdd
// 等)经 rbx+disp32 直接读/写值栈槽位。
//
// 实参:
//
//	codeAddr  ← mmap 段起点(PROT_RX)
//	jitCtx    ← *JITContext 转 uintptr(经 unsafe.Pointer)
//	vsBase    ← valueStackBase(arena.Words 起点 + base*8)
//
// 实现:trampoline_spec_amd64.s::callJITSpec(NOSPLIT|NOFRAME)。
//
//go:noescape
func callJITSpec(codeAddr uintptr, jitCtx uintptr, vsBase uintptr) uint64

// CallJITSpec 是 callJITSpec 的可见包装。
func CallJITSpec(codeAddr uintptr, jitCtx uintptr, vsBase uintptr) uint64 {
	return callJITSpec(codeAddr, jitCtx, vsBase)
}
