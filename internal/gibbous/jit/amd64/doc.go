//go:build wangshu_p4 && amd64

// Package amd64 实装 P4 amd64 后端发射器。
//
// **PJ0 包骨架**:仅 doc.go 占位;PJ1 起填 Emitter trait + trampoline asm
// stub + 直线模板(MOVE/LOADK/LOADBOOL/LOADNIL/JMP/RETURN)。
//
// 寄存器约定(`docs/design/p4-method-jit/06-backends.md` §4.1,P4 单一事实源):
//   - r15:jitContext(常驻,trampoline 切 SP 时装入)
//   - r14:arena base(常驻,jitContext 镜像)
//   - rbx:值栈 base(per-call 装入,OSR exit 时写回 jitContext.exitBase)
//   - rax-r9:暂存(per-template 用)
//   - xmm0-xmm3:f64 快路径用
//
// Go ABI0 兼容性:r12-r15(callee-saved)+ rbp(frame pointer)需 trampoline
// 进入时保存、退出时恢复;Go runtime 的 GS/FS 段寄存器不动。
package amd64
