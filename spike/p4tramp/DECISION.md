# P4 PJ1 spike 决策报告

> **状态:🟢 全绿,2026-06-25 完成**(承
> `docs/design/p4-method-jit/06-backends.md` §1.7 spike 检查纪律 +
> `docs/design/p4-method-jit/00-overview.md` §4 PJ1 行)。
>
> **裁决:PJ1 emitter trait + amd64 trampoline asm 完成解锁**。

## 测量环境

- Intel Xeon 6982P-C / Linux 6.8.0 amd64 / Go 1.26.2
- 独立 go module(`spike/p4tramp/`,镜像 `spike/p3boundary` / `spike/p3indirect`,
  不污染主库零外部依赖纪律)

## 检查四档 + 实测

| 检查 | 内容 | 实测 | 判定 |
|---|---|---|---|
| ① | exec mmap + W^X 翻面 | unix.Mmap PROT_RW alloc → 写入 9 字节 「mov rax, imm64; ret」 → unix.Mprotect PROT_RX | ✅ 工作 |
| ② | Go → mmap 段 → ret 对称性 | TestSpike_S2 同段 10000 次 repeated CALL 返回值稳定;TestSpike_S3 8 段 100 轮交叉 CALL 段间无串扰 | ✅ 工作 |
| ③ | 单条直线模板可发射可执行 | TestSpike_S1 5 档 imm64(0 / 1 / 0xdeadbeef / 0xcafebabedeadbeef / ^uint64(0))全过 | ✅ 工作 |
| ④ | 单 CALL 成本上限 | **1.95 ns/op**(`-benchtime=2s -count=3` 中位数;比 P3 wazero S1 18.9ns 快 ~10x) | ✅ 远低于 P3 边界 |

## 与 P3 spike 对照

| 维度 | P3 wazero S1(空往返) | P4 mmap CALL | 收益来源 |
|---|---|---|---|
| ns/op | 18.9 | **1.95** | P4 自管 codegen 不经 wazero/Wasm 中介(承 05-system-pipeline §0.1) |
| 调用通道 | wazero CompiledFunction.Call | 直接 amd64 CALL 指令 | 无 trampoline 序言 / 参数解包 / Wasm 栈帧建立 |
| 物理基础 | wazero 自管栈 + 异步抢占协作终止 | mmap 段 PROT_RX + Go ABI0 CALL | P4 端 W^X + Go runtime asm 协议 |

**P4 自管 codegen 的物理收益首次实证**:~10x 单 CALL 加速,虽然这是 spike 极简
形式(无 jitContext / 无切 SP / 无 callee-saved 保存),完整 PJ1 trampoline 加上
这些后预期单 CALL ~5-10ns(估算,留 PJ1 完整版实测复核)。

## spike 极简形式的限制(承"做什么 / 不做什么"纪律)

本 spike **不验**:
- 自管机器栈切换(切 SP 进 jitContext-bound 自管 `[]byte` 栈)——留 PJ1 完整版;
- callee-saved 寄存器保存恢复(`r12-r15/rbp` Go ABI0 协议)——留 PJ1;
- jitContext 装载(`r15 = jitContext / r14 = arena base`,06 §4.1)——留 PJ1;
- 多 goroutine 并发调度下的 mmap 段共享(本 spike 单 goroutine 测)——留 PJ1;
- darwin/arm64 W^X(MAP_JIT + pthread_jit_write_protect_np)——留 PJ8 启动前 spike;
- icache flush(arm64,linux/amd64 硬件保证一致性)——留 PJ8。

**spike 检查纪律**(06 §1.7):本 spike 只验「PJ1 emitter trait 签名完成的物理
基础成立」——三档 ✅ + 单 CALL 成本基线 → emitter trait 设计可承诺合理成本上限。

## 红灯退路(若 spike 失败时的应急方案)

(本次未触发,留作未来回归依据。)

| 失败档 | 退路 |
|---|---|
| ① mmap RW + mprotect RX 失败 | 检查 SELinux / capability;改用 memfd_create + mmap 替代(linux 5.x+) |
| ② CALL 段后 Go runtime SEGV | 改用 `syscall.RawSyscall6` 间接调用模式;或上 `runtime/cgo`-bound trampoline |
| ③ 段返回值乱码 | 检查 amd64 编码字节序 / RAX 是否被 Go ABI0 占用 |
| ④ 单 CALL > P3 wazero S1 (18.9ns) | 重评 `06-backends.md` §1 候选裁决——共享骨架 + per-arch vs 宏汇编 |

## 版本绑定提醒

- 数据随 Go 1.26.2 / linux/amd64 / 内核 6.8 记录;
- 未来 Go 版本升级 / 内核升级若改 mmap 行为或 ABI,需重跑 spike(`spike/p4tramp/` 保留作回归);
- 与 P3 spike 一样的维护协议(承 spike/p3boundary/DECISION.md 范本——本仓暂未独立写,P3 实测数据存档在 `docs/design/p3-wasm-tier/implementation-progress.md` §0.1)。

## 后续动作

🟢 **PJ1 emitter trait + amd64 trampoline asm 完成**:

1. `internal/gibbous/jit/amd64/codepage.go`(对位本 spike `mmap_linux_amd64.go`,
   加 codePagePool + per-Proto 段释放策略);
2. `internal/gibbous/jit/amd64/trampoline_amd64.s`(对位本 spike
   `trampoline_amd64.s`,加切 SP / 装 jitContext / 保存 callee-saved);
3. `internal/gibbous/jit/jitcontext.go`(jitContext struct 字段表,承 05 §3.3);
4. `internal/gibbous/jit/amd64/emitter.go`(Emitter trait 实现,承 06 §2.4);
5. `internal/gibbous/jit/amd64/templates.go`(直线模板:MOVE / LOADK / LOADBOOL /
   LOADNIL / JMP / RETURN,承 06 §3.7 + §3.4 + §3.5);
6. PJ1 完整版回归测试:本 spike 测试套作 PJ1 子集保留(承 prove-the-path-under-test
   纪律,白盒断言 mmap 段确实被走到)。
