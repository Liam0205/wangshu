//go:build wangshu_p4 && arm64

// Package arm64 实装 P4 arm64 后端发射器。
//
// **PJ0 包骨架**:仅 doc.go 占位;PJ8 启动时填 Emitter trait per-arch 镜像
// + arm64 trampoline asm stub + 各 opcode 模板。
//
// 寄存器约定(`docs/design/p4-method-jit/06-backends.md` §4.2,P4 单一事实源):
//   - x28:jitContext(常驻;Go runtime 的 G 寄存器特殊处理 — 注 §4.2)
//   - x27:arena base
//   - x26:值栈 base
//   - x0-x9:暂存
//   - v0-v3:f64 快路径
//
// macOS arm64 W^X:MAP_JIT + pthread_jit_write_protect_np(无 wazero 实装
// 先例,PJ1 同步 spike,详 §2.2.4)。
package arm64
