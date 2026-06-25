// Package p4tramp 是 P4 PJ1 spike——Go → mmap 出来的 amd64 → ret 回 Go 的最小
// round-trip 实证(承 docs/design/p4-method-jit/06-backends.md §1.7 闸门纪律)。
//
// 不污染主库:独立 go module(类比 spike/p3boundary / spike/p3indirect 同款)。
//
// 实证目标(承 06 §6.1 PJ1 验收):
//
//   - 闸门 ① exec mmap + W^X 翻面工作:syscall.Mmap PROT_READ|PROT_WRITE →
//     unix.Mprotect PROT_READ|PROT_EXEC,然后从 Go 跳进去执行;
//   - 闸门 ② trampoline 进/出对称:Go → asm stub → 自管栈 → mmap 段 → ret →
//     恢复 Go 栈;不踩 Go runtime(GS/FS 段不动,callee-saved 寄存器保存恢复);
//   - 闸门 ③ 单条直线模板可发射 + 可执行:发射「mov rax, IMM64; ret」八字节
//     序列,Go 调进去拿到 IMM64 值——这是 LOADK 模板的物理基础;
//   - 闸门 ④ exec mmap 单次成本上限实证:与 P3 spike S1/S2/S3 同款形态,产出
//     ns/op 数字进档作为 PJ1+ 性能基线。
//
// **闸门红绿决策**(失败即返工):
//   - 绿(全部 ①②③④ 通过):PJ1 emitter trait 签名 + amd64 trampoline asm 落地;
//   - 红(任一失败):本节实测细节回反 06 §2.4 / §4.1 落地形态,可能改寄存器
//     约定 / 进入协议 / W^X 时序;不允许在 spike 红的状态下推 emitter trait。
//
// 平台覆盖(PJ1 spike 范围):
//   - linux/amd64:基础闸门(本 spike 主体);
//   - darwin/arm64:W^X 形态 spike(MAP_JIT + pthread_jit_write_protect_np vs
//     RW→RX seal 二折一)留 PJ8 启动前补;PJ1 主助理裁决项 ③ 已登记。
//
// 上游契约:
//   - docs/design/p4-method-jit/06-backends.md §1.7 spike 闸门纪律
//   - docs/design/p4-method-jit/05-system-pipeline.md §2 系统四件套(exec mmap /
//     W^X / icache / trampoline)
//   - spike/p3boundary、spike/p3indirect:P3 同款 spike 范本(独立 module / 三档
//     样本 / 决策报告归档)
package p4tramp
