# 在生产环境嵌入 P3 / P4:部署要求、运行期开关与观测

> 读者:准备在生产环境启用 wangshu 分层执行(`wangshu_p3` wasm 层或 `wangshu_p4` 原生 JIT 层)的嵌入方。只用默认 build(P1 解释器)的用户不需要读本文——P1 没有额外的部署要求。
>
> 设计文档侧的对应材料:[design/architecture.md](./design/architecture.md)(分层结构)、[design/p4-method-jit/](./design/p4-method-jit/)(P4 详细设计)。本文只讲嵌入方需要知道的部分。

## 1. 选层:build tag 决定档位

| 档位 | build tag | 执行方式 | 部署要求 |
|---|---|---|---|
| P1 crescent | (默认) | 纯 Go 解释器 | 无 |
| P3 gibbous-wasm | `wangshu_p3 wangshu_profile` | wazero 解释 wasm | 无(纯 Go) |
| P4 gibbous-jit | `wangshu_p4 wangshu_profile` | mmap 可执行页 + 原生机器码 | 见 §2 |

- `wangshu_profile` 是升层前置:不带它热度采样整段编译期消去,P3/P4 tag 形同虚设。
- `wangshu_p3` 与 `wangshu_p4` 互斥。
- P4 只支持 linux/darwin 的 amd64 + arm64;其他平台请用 P3 或 P1。
- 无论哪档,**解释器永不退役**:升不了层的形状(vararg、协程、不支持的 opcode)自动留在 P1 执行,输出与解释器逐字节一致(这是每 4 小时一轮的差分 fuzz 持续验证的口径)。

## 2. P4 的部署要求:可执行内存

P4 在运行期通过 `mmap(PROT_READ|PROT_WRITE)` 分配代码页、写入机器码后 `mprotect` 翻到 `PROT_READ|PROT_EXEC`(W^X 纪律,写与执行权限不同时持有)。这在多数环境开箱即用,但以下环境需要确认:

- **seccomp 严格模式 / 自定义 profile**:需要放行 `mmap` + `mprotect`(含 `PROT_EXEC` 参数)。Docker 默认 profile 放行;`seccomp=strict` 或自定义收紧的 profile 可能拦截。
- **SELinux**:`deny_execmem` 布尔值开启时,匿名可执行映射被拒。需要 `allow_execmem` 或对进程域放行 `execmem`。
- **PaX / grsecurity MPROTECT**:阻止「先可写后可执行」的翻转,P4 无法工作。
- **iOS / 部分沙盒平台**:无 JIT entitlement 时不可用(本项目不以这些平台为目标)。

被拦截时的失败形式是 mmap/mprotect 报错 → 该 Proto 编译失败 → 落到 `TierStats.StuckCompileFailed`(见 §4),脚本仍由解释器正确执行——**不会崩溃,但也没有 JIT 收益**。部署到收紧环境前,建议用 §4 的观测接口在预发验证 `Promoted > 0`。

内存开销的量级:每个升层 Proto 一段代码页(典型几 KiB)+ 一段 64 KiB 自管 spill 栈。数百个热函数的场景总开销在 MB 量级。

## 3. 运行期开关:一键退回解释器

`SetTierEnabled` 是生产 admin API(与 testing-only 的 `SetForceAllPromote` 不同),用于灰度期间不重启进程就把某个 State 降回纯解释执行:

```go
st := wangshu.NewState(wangshu.Options{})

// 线上怀疑升层路径有问题:立刻关掉
st.SetTierEnabled(false)
// 之后的执行全部走 P1 解释器——包括已经升层的函数;
// 正在原生段内执行的那一次调用会正常跑完,下一次分派起生效。

// 排查完毕:恢复分层执行(编译产物还在缓存里,不需要重新编译)
st.SetTierEnabled(true)
```

语义要点:

- 关闭后**新升层停止**(热度采样直接短路,也不再累积热度);
- 关闭后**已升层的函数也回解释器**——这是与「只挡新升层」的关键差别,保证降级是完整的;
- 开关是 State 级的。池化场景想全量降级,对池里每个 State 调一次(State 不跨 goroutine 共享,所以没有全局开关);
- 默认 build 下没有层可关,开关是 no-op,`TierEnabled()` 恒返 `true`。

## 4. 观测:TierStats

`TierStatsSnapshot()` 返回 State 级分层执行分布,用于回答「升层是否符合预期」「有没有值得排查的失败」:

```go
stats := st.TierStatsSnapshot()
// stats.Promoted            已升层 proto 数
// stats.StuckNotCompilable  形状被排除(vararg/协程/不支持 opcode)——预期内
// stats.StuckDeclined       后端判不划算放弃——预期内
// stats.StuckCompileFailed  真编译失败(报错/panic)——非零值得排查
// stats.Profiled            有 profile 数据的 proto 数
// stats.TierEnabled         运行期开关状态
```

生产接入建议:

- 把快照接进指标系统(周期性采集即可,开销是几次计数读取;不要逐帧轮询);
- **告警条件:`StuckCompileFailed > 0`**——两类 Stuck(NotCompilable / Declined)是设计内行为,编译失败不是;
- 预发验证:预期的热函数跑热之后 `Promoted` 应该 > 0,恒 0 说明升层没发生(build tag 缺失、环境拦截 exec-mmap、或负载形状全被排除),此时 P4 build 的数字就是解释器数字;
- 配合 §3 的开关做对照实验:怀疑某个性能异常来自升层路径时,`SetTierEnabled(false)` 之后对比前后指标。

## 5. Step budget 在分层执行下的语义

`SetStepBudget` 的语义在所有档位一致:超出预算的脚本以 `instruction budget exceeded` 错误终止。它是**工作量预算**,不是纯指令计数——每个 preempt 点(循环回跳、调用/帧入口、TFORLOOP)计一个单位,此外 bulk 字符串操作按搬运的字节数额外计费:CONCAT 计 `len(result)>>6`(1 单位 / 64 字节),使 byte-heavy 的 concat 无法在小预算内无界运行(见 `internal/crescent` 的 `chargeBulkWork`,issue #166/#167)。单次约 1 MiB 的 concat 计约 16K 单位;小于 64 字节的 concat 额外计 0。分层执行下实现方式不同,精度有差别:

- P1:在每个 preempt 点(函数/帧入口、负向 `JMP` / `FORLOOP` 循环回跳、`TFORLOOP`)精确计一个单位,并额外计 CONCAT 字节工作量;普通直线 opcode(`MOVE`/`ADD` 等)不逐条计费。它是各档里 preempt 点计量最精确的,但**不是逐指令计量**——不要据此按指令数计费;若确需逐指令口径,应作为独立能力单独实现和测试;
- P3/P4:升层代码内不逐指令计数,改为在**函数入口、调用点、循环回边**按燃料(fuel)计费——每段进入时预支一段配额,回边/调用耗尽时结账并检查预算;CONCAT 字节计费经共享 `doConcat` 对所有档位一致生效;
- 因此 P4 下预算的触发点有**一段配额的粒度误差**(不会无限超支;纯算术死循环、自尾递归死循环都会被回边燃料按期截停);
- `context.Context` 取消同理:在回边/调用检查点生效,不是逐指令生效。

预算值因此在**不同档位与不同负载之间是近似值**,不是精确指令数。如果业务只是把预算当作「防脚本挂起 / byte-heavy 风暴」的兜底(绝大多数场景),各档粒度完全够用。没有哪一档提供逐指令精确计量:P1 的 preempt 点计量最细,但直线 opcode 不计费、CONCAT 按字节计费,所以任何按指令数计费的口径都需要另行实现。

## 6. 上线检查清单

1. build 带对 tag(`wangshu_p4 wangshu_profile`),CI 里对该 build 跑测试;
2. 目标环境验证 exec-mmap 可用(预发跑真实负载,确认 `TierStats.Promoted > 0` 且 `StuckCompileFailed == 0`);
3. 接好 `TierStatsSnapshot` 指标采集与 `StuckCompileFailed` 告警;
4. 降级预案演练:验证 `SetTierEnabled(false)` 后业务结果不变、延迟符合解释器档预期;
5. 灰度顺序建议:先小流量开 P4,观察一至两周(nightly fuzz 的发现节奏参考:上一个正确性问题距今多久),再放量。
