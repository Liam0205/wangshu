# P2 §6:验收测试策略 —— 决策正确口径总表 / 误判注入 / 升层日志 / 跨阶段差分

> 状态:**设计阶段,详细设计已齐备**。本文是 [00-overview](./00-overview.md) §0 文档地图列出的 **P2 验收单一事实源**——P2 验收口径总表(决策正确,非性能)、可编译性误判注入测试、升层日志断言、编译失败 fallback 对拍、跨阶段差分(crescent-only vs P2-on-crescent 同结果)、P1-only build tag 退化对拍。
> 上游种子:[00-overview](./00-overview.md) §8 不变式 + §9 缺口。
> 上游耦合面:[00-overview](./00-overview.md) §4 PB7 验收(完成定义)、§8 P2 的成功标准是「决策正确」、§9 风险与未决缺口。
> P1 依赖面:[../p1-interpreter/12-testing-difftest](../p1-interpreter/12-testing-difftest.md)(P1 验收口径总表 §10 是本文的形态参考;但 P1 是性能 + byte-equal 口径,P2 是决策正确 + 退化等价口径,**两者并行不替换**——见本文 §9)。
> 上游原则面:[../roadmap.md](../roadmap.md) §5 原则 2(层间逐字节差分测试)+ 原则 3(每阶段独立交付)。
>
> **本文定位一句话**:**P2 不加速,所以没有性能门**——但 P2 的「成功」不是「跑得多快」而是「决策正确」(不漏报真热点 / IC 反馈忠实反映运行期 / **可编译性绝不误判**)。本文的核心是把 P2 的不变式翻成可执行的验收断言,每条口径都对应一组测试,**不留无法验证的承诺**。

---

## 0. 定位:P2 验收三轴(不加速没有性能门,但有「决策正确」三轴)

### 0.1 P2 与 P1/P3/P4 验收的根本不同

承 [00-overview](./00-overview.md) §8 的对照表,P2 是整条演进流水线里**唯一没有性能验收门**的阶段——这一性质来自 P2 的基建定位([00-overview](./00-overview.md) §1):**「P1 产料,P2 加工成决策,P3 消费决策去加速。P2 是中间的决策加工厂,自己不进热路径」**。

把四个阶段的验收口径并列,P2 的特异性一目了然:

| 阶段 | 性能验收 | 正确性验收 | 主防线 |
|---|---|---|---|
| P1 | ≥2x over gopher-lua 三档 + benchmark game 五项 + boundary mini([12 §6](../p1-interpreter/12-testing-difftest.md)) | 三方差分逐字节一致([12 §3](../p1-interpreter/12-testing-difftest.md)) | **byte-equal 差分 fuzz**(防 codegen / IC / GC 透明性偏差) |
| **P2**(本文) | **❌ 无**(基建定位) | ✅ **唯一验收维度** | **可编译性零误判注入 fuzz**(防安全闸门失守) |
| P3 | 循环密集 ≥2x over P1 | crescent/gibbous 层间差分(P3 §测试) | 层间 byte-equal(crescent 与 gibbous 同输入同输出) |
| P4 | 列内核 ≥ luajc 档 | 投机正确性(deopt 着陆点严格性) | 投机 + deopt 差分(P4 §测试) |

P1 的主防线是 byte-equal 差分——把「投机错误静默错果」(roadmap §5 原则 2 红线)挡在编译期 + 运行期。**P2 的主防线对应物是「可编译性零误判注入 fuzz」**——把「静态闸门把不可编译形状判成可编译」这类灾难挡在 Compile 期。两者地位等同:一旦放过一个误判,后果都不是「跑慢点」,而是「正确性崩溃」(详见 [03 §0.1 / §1.1](./03-compilability-analysis.md))。

### 0.2 P2 验收三轴

承 [00-overview](./00-overview.md) §8 末尾的「P2 验收三条主轴」,本文把它们翻成具体口径分类(本文 §1 总表的轴):

1. **可编译性零误判**(本文 V1-V7,**主防线**)——F1-F7 形状的零误判注入 fuzz 是 P2 的安全核心。任何让「不可编译形状判成可编译」的代码或文档都直接判否。这一轴对应 P1 的 byte-equal 差分轴。
2. **累积合理 + 阈值生效**(本文 V8-V10)——回边/入口计数累积非零、越阈值才触发 considerPromotion、单调上升不丢失;profile 开关切换 byte-equal、TierStuck 后停止累积。
3. **状态机不变量**(本文 V11-V13)——TierStuck 不再触发(防抖)、升层单向(无 Gibbous→Interp 边)、profileTable 跨 State `-race` 通过。

外加三组横向轴(每条 P2 不变式都要可验证):

4. **跨 State 并发**(本文 V14-V16)——多 State 并发 profileTable 的 `-race` 通过 / 多 State 累积 / sync.Pool 复用形态(承 [01-profiling §6](./01-profiling.md))。
5. **升层日志**(本文 V17-V19)——三类日志格式的逐字节匹配 / 落点是 gibbous 而非 bridge / 诊断接口 race-safe。
6. **跨阶段差分**(本文 V20-V22,**核心承诺**)——P2 上线前后跑同一 Lua 脚本输出 byte-equal(P2 不加速也不影响输出);build tag 关 P2 完全退化等价 P1。

**总六轴二十二条口径**(V1..V22),覆盖 [00-overview](./00-overview.md) §8 的全部不变式与各 P2 子文档(01..05)的全部不变式。详见 §1 总表。

### 0.3 「不留无法验证的承诺」

本文的核心铁律(承 [00-overview](./00-overview.md) §8 不变式工程纪律):**每条 P2 不变式都必须对应一组可执行测试**——若一条不变式只有「设计期应当」的措辞而无运行期验证,该不变式视为**未交付**。

这与 P1 [12 §0](../p1-interpreter/12-testing-difftest.md) 的「最终法庭」思想同构——P1 把所有「待 12 定」的口径在 [12 §10](../p1-interpreter/12-testing-difftest.md) 一张表收口;P2 把所有「不变式应当如何如何」的承诺在本文 §1 一张表收口,每条都给出测试入口、注入器、自动化检查方法。

### 0.4 与下游的接口(本文产出什么)

本文的输出是 PB7 验收测试套清单(详见 §8)。具体地,`make test-p2` 一键跑过的内容:

```
test/p2/                           ← P2 测试套根目录(本文 §8 主管)
├── compilability_inject/          ← V1-V7 可编译性零误判注入 fuzz
│   ├── f1_vararg_test.go
│   ├── f2_coroutine_test.go
│   ├── f3_debug_test.go
│   ├── f4_setfenv_test.go
│   ├── f5_oversize_test.go
│   ├── f6_closure_test.go
│   ├── f7_backend_test.go
│   └── inject_fuzz_test.go        ← 综合注入 fuzz 入口
├── profiling/                     ← V8-V10 计数与阈值
│   ├── accumulate_test.go
│   ├── threshold_test.go
│   └── byte_equal_test.go         ← profileEnabled 切换 byte-equal
├── tierstate/                     ← V11-V13 状态机不变量
│   ├── transition_test.go
│   ├── stuck_no_retry_test.go
│   └── unidirectional_test.go
├── concurrency/                   ← V14-V16 跨 State 并发
│   ├── race_test.go               ← `go test -race` 触发
│   ├── multi_state_test.go
│   └── pool_reuse_test.go
├── promotion_log/                 ← V17-V19 升层日志
│   ├── log_format_test.go
│   ├── log_target_test.go         ← 落点必须是 gibbous,非 bridge
│   └── diag_race_test.go
├── cross_phase_diff/              ← V20-V22 跨阶段差分(核心)
│   ├── crescent_only_vs_p2_test.go
│   ├── mock_p3_vs_real_p3_test.go
│   └── build_tag_p1only_test.go   ← !profile build tag 退化等价
└── e2e/                           ← PB7 端到端验收
    └── pb7_acceptance_test.go     ← 跑齐 V1-V22 一键过/不过
```

---

## 1. P2 验收口径总表(本文最有价值的产出 —— 逐条收口)

下表对应 P1 [12 §10 验收口径总表](../p1-interpreter/12-testing-difftest.md)的形态——把 P2 全部不变式拉成「类目 / 断言 / 单测对应 / 注入 fuzz / 自动化检查」五栏,逐条钉死。**这张表是本文存在的核心理由**:它把散落在 P2 各子文档的不变式(00 §1 / 01 §7 / 02 §8 / 03 §8 / 04 §6 / 05 §不变式)集中在一处,并给每条配可执行的检查路径。

### 1.1 总表(V1-V22)

| # | 类目 | 断言(P2 不变式) | 单测对应 | 注入 fuzz | 自动化检查 |
|---|---|---|---|---|---|
| **V1** | 可编译性零误判 / F1 vararg | 任一 `FuncExpr.IsVararg=true` 或体含 `...` 表达式或 Proto.IsVararg=true 或 Code 含 VARARG opcode → `analyzeProto` 必判 `CompNotCompilable` | `compilability_inject/f1_vararg_test.go`(table-driven 5 形状) | `inject_fuzz_test.go`「随机生成含/不含 vararg 的脚本,断言判定与三重检查一致」 | `analyzeProto(scriptWithVararg) == CompNotCompilable` 必断言 |
| **V2** | 可编译性零误判 / F2 协程 | 函数体直接调 `coroutine.yield/resume/wrap/create/...` 或调 unknown 函数 → 必判 `CompNotCompilable` | `f2_coroutine_test.go`(覆盖 §3.2.4 (1)-(4) 写法) | 「合成脚本注入 coroutine.* 调用 + 间接调用,断言全部判 NotCompilable」 | `analyzeProto(scriptWithYield) == CompNotCompilable` |
| **V3** | 可编译性零误判 / F3 debug | 任一 `NameExpr{"debug"}` → 必判 `CompNotCompilable` | `f3_debug_test.go`(debug.traceback / getlocal / sethook / 重命名) | 「合成脚本随机插入 debug.* 引用,断言判定不变」 | 同上 |
| **V4** | 可编译性零误判 / F4 setfenv | 任一 `NameExpr{"setfenv"/"getfenv"}` → 必判 `CompNotCompilable` | `f4_setfenv_test.go` | 「合成脚本插入 setfenv / getfenv,断言判定不变」 | 同上 |
| **V5** | 可编译性零误判 / F5 过大 | `len(Proto.Code) > MaxCompilableInsns(2000)` 或 `MaxStack > 200` → 必判 `CompNotCompilable` | `f5_oversize_test.go`(边界值 1999/2000/2001 + reg 199/200/201) | 「生成不同尺寸函数,断言阈值边界判定」 | 边界值 table-driven |
| **V6** | 可编译性零误判 / F6 嵌套 | 嵌套深度 > 3 或 upvalue 数 > 8 → 必判 `CompNotCompilable` | `f6_closure_test.go`(深度 3/4 + upval 8/9 边界) | 「生成不同嵌套深度的脚本,断言判定」 | 边界值 table-driven |
| **V7** | 可编译性零误判 / F7 + 综合 | `b.p3.SupportsAllOpcodes(proto)=false` → 必判 `CompNotCompilable`;**综合**:F1-F7 任一组合触发都必判,**永不放过** | `f7_backend_test.go` + `inject_fuzz_test.go` 综合 | **核心 fuzz**:随机选 F1-F7 子集注入,断言任一注入即 NotCompilable;反向:F1-F7 全不注入的纯算术脚本断言 Compilable | `inject_fuzz_test.go` 1e6 次,失败即视为正确性事故(主防线) |
| **V8** | 计数累积合理 | onBackEdge / onEnter 调用后,对应 `pd.backEdge[pc]` / `pd.entryCount` 单调上升;TierStuck/TierGibbous 后停止累积 | `profiling/accumulate_test.go`(一组合成脚本,逐次跑后断言计数值) | — | 跑脚本 N 次后断言 `pd.backEdge[pc] == N` 等(具体 pc 由黄金字节码确定) |
| **V9** | 阈值生效 | `pd.backEdge[pc] >= HotBackEdgeThreshold`(1000)时 `considerPromotion` 被调用一次;`<` 时不调用;`>=` 后再调入 onBackEdge,**considerPromotion 仅触发一次**(幂等) | `threshold_test.go`(边界值 999/1000/1001/2000) | — | mock considerPromotion 注入计数器,断言 callCount 等于预期 |
| **V10** | 编译预算 / profile 开关切换 byte-equal | `vm.profileEnabled=false`(P1-only)与 `=true`(P2)跑同一脚本输出**逐字节相等**;且 P1 不调 onBackEdge / onEnter | `profiling/byte_equal_test.go` | — | 同输入两次跑,`bytes.Equal(out_p1only, out_p2)` 必为 true |
| **V11** | 状态机单向 | TierState 只有边 `Interp→Gibbous` / `Interp→Stuck`,**无 `Gibbous→Interp` 或 `Stuck→*`**;table-driven 全部状态对断言无非法转移 | `tierstate/transition_test.go`(table-driven 9 状态对) | — | 状态机 visitor 列举所有 (from, to) 对,断言只允许定义中的 |
| **V12** | TierStuck 不重试 | 一旦 `tierState=TierStuck`,后续 onBackEdge/onEnter 计数仍累积(直到守卫拦下;详见 §4)但 considerPromotion **永不被再调用** | `tierstate/stuck_no_retry_test.go`(脚本注入 F2,跑 N>10000 次,断言 considerPromotion callCount==1) | — | mock P3 注入失败,断言后续 N 次回边后 callCount 不再增 |
| **V13** | considerPromotion 幂等 | 多次调用 considerPromotion(同一 Proto)第二次起立刻 no-op(`pd.tierState != TierInterp` 守卫) | `tierstate/transition_test.go` 子测 | — | 直接调 considerPromotion 二次,断言第二次 no-op |
| **V14** | profileTable `-race` 通过 | 多 goroutine 并发跑同一 Program(各自 State),`go test -race` 不报 race | `concurrency/race_test.go`(`go test -race -count=10`) | — | CI 跑 `-race`,失败即破 |
| **V15** | 多 State 累积独立 | 多个 State 跑同一 Program,各自 `pd.backEdge[pc]` 独立累积,**无跨 State 共享** | `concurrency/multi_state_test.go` | — | 两 State 各跑 N 次,断言两 profileTable 各自 N |
| **V16** | sync.Pool 复用 State | State 经 Pool Reset/Get,profileTable 在 Reset 后清空(若按 §6.4 (B) 方案);(C) 启用后累积入 Proto 旁聚合表 | `concurrency/pool_reuse_test.go`(B 方案行为锁定 + (C) 启用前的限制行为) | — | Pool Get/Put 一轮后 profileTable 应空(B);(C) 启用后断言聚合表非空 |
| **V17** | 升层日志格式(三类) | `function <name> promoted to gibbous` / `function <name> stays interpreted (not compilable: <reason>)` / `function <name> compile failed, stays interpreted: <err>` 三条文本格式逐字节匹配 | `promotion_log/log_format_test.go`(三场景注入 + 抓 diag 输出 + 正则) | — | 抓 `diag` 输出,逐字节匹配 |
| **V18** | 升层落点是 gibbous 非 bridge | 日志里 `promoted to <X>` 的 X **永远是 `gibbous`,不是 `bridge`**(P2 不是执行层) | `promotion_log/log_target_test.go`(grep 全部日志,任一含 `promoted to bridge` 即破) | — | 跑全 P2 测试 + 抓 diag,grep `promoted to bridge` 必为空 |
| **V19** | 诊断接口 race-safe | 多 goroutine 升层并发触发 diag 输出,`-race` 不报 race;diag 接口本身用 mutex 或 channel | `promotion_log/diag_race_test.go`(`-race -count=10`) | — | `-race` |
| **V20** | 跨阶段差分:crescent-only vs P2-on-crescent | 同一 Lua 脚本在「P1-only」与「P2 启用但 mock P3 永远拒编译」两条路径下输出**逐字节相等** | `cross_phase_diff/crescent_only_vs_p2_test.go`(三档 + benchmark game 五项 + boundary mini) | — | 同输入两路径输出 byte-equal(承 P1 [12 §6](../p1-interpreter/12-testing-difftest.md) 三档 + 五项 + boundary mini)|
| **V21** | mock P3 vs 真 P3 行为差异度量 | mock P3(返回 dummy GibbousCode 但实际仍走解释器)与真 P3 在「升层成功」分支上行为差异;**当前缺口**:真 P3 上线前 mock 的 dummy 应实现「立即返回错误强制 Stuck」与「返回 dummy 但走解释」两种行为,二者输出 byte-equal 于 P1-only | `cross_phase_diff/mock_p3_vs_real_p3_test.go`(P3 上线前部分跳过) | — | mock 两形态 + P1-only 三路径 byte-equal |
| **V22** | P1-only build tag 退化等价 | `go test -tags '!profile'` 编译望舒,P2 整个包不存在(`internal/bridge` 不被引用),跑测试与 P1 行为完全等价 | `cross_phase_diff/build_tag_p1only_test.go` | — | `go build -tags '!profile'` 成功 + 跑差分套与 P1-only 一致 |

### 1.2 收口统计

本表收口 **22 条**口径,覆盖:

- **V1-V7(7 条)**:可编译性零误判,P2 主防线(对应 [03](./03-compilability-analysis.md) F1-F7 + 综合)
- **V8-V10(3 条)**:计数与阈值(对应 [01](./01-profiling.md) §1-§5)
- **V11-V13(3 条)**:状态机不变量(对应 [04](./04-try-compile-fallback.md) §2-§3 + [01 §4.3](./01-profiling.md))
- **V14-V16(3 条)**:跨 State 并发(对应 [01 §6](./01-profiling.md))
- **V17-V19(3 条)**:升层日志(对应 [04 §6](./04-try-compile-fallback.md) 升层日志格式)
- **V20-V22(3 条)**:跨阶段差分(P2 设计承诺的核心:**P2 不影响 P1 输出**)

**P4 build 下 P2 V1-V22 仍跑**(承 [P4 08 §0.2](../p4-method-jit/08-testing-strategy.md) 字面承诺 + [P4 implementation-progress §2 RJ-13](../p4-method-jit/implementation-progress.md) 跨文档回填请求):P4 build(`wangshu_p4` + `wangshu_profile`)下本表 V1-V22 全套不豁免。P2 是 P3/P4 共享前端,P2 主防线纪律(可编译性零误判 / 状态机单向 / 计数累积合理 / 跨 State 并发 / 升层日志格式)在 P4 build 下仍是验收硬门。具体接入路径:`make test-p4` 套件含 P2 V1-V22 单测全过 + V14 -race 通过 + V17-V19 升层日志格式不变。

### 1.3 与 P1 [12 §10] 验收口径总表的关系

P1 [12 §10](../p1-interpreter/12-testing-difftest.md) 收口 26 条**口径问题 → 最终决策**(pairs 序 / 数字格式 / 措辞 / GC 透明性...)。它是**「不一致的各种现象如何处理」**的收口表。

本文 §1 收口 22 条**断言 → 检查路径**。它是**「P2 设计承诺如何被验证」**的收口表。

形态对偶,但内容互补:

| 维度 | P1 [12 §10] | 本文 §1 |
|---|---|---|
| 收口对象 | 各种现象的口径决策 | 各条不变式的检查路径 |
| 主防线 | byte-equal 差分(防投机错果) | 可编译性零误判注入 fuzz(防安全闸门失守) |
| 表项含义 | 「这种情况要 X」 | 「这条断言由 Y 测试覆盖」 |

**两者并行不替换**——P2 上线后,P1 [12 §10] 的 26 条口径仍要在 P2 启用 build 下全部跑过(差分套不会因为 P2 引入而豁免);本文 §1 是 P2 新增的 22 条断言,只在 P2 启用 build 下被检查。详见 §9。

---

## 2. 可编译性零误判注入测试(V1-V7) —— P2 主防线

### 2.1 主防线的工程定位

承 [03 §0.1 / §1.1](./03-compilability-analysis.md):**「把不可编译形状判成可编译,P3 会编译出错误代码或运行期崩溃,而 fallback 机制根本不会被触发」**——这是 P2 的「投机错误静默错果」对应物。本节的 V1-V7 注入测试是把这条铁律翻成可执行验证的核心工作。

测试组织原则:

1. **每条 Fk 形状**(F1-F7)**对应一个独立测试文件**(`f1_vararg_test.go` / `f2_coroutine_test.go` / ...),保证「某条规则放宽」时只动一个文件。
2. **每个文件含 table-driven 表 + 注入 fuzz 子测**——前者覆盖典型形状(已知会触发 Fk 的 N 种写法),后者由 fuzz 引擎随机生成包含 Fk 形状的脚本断言判定。
3. **综合 fuzz**(`inject_fuzz_test.go`)随机选 F1-F7 子集注入,覆盖「多形状叠加」边角(如 F1 + F4 同时触发,或 F2 + F5 边界值)。

### 2.2 F1 vararg 注入测试

#### 2.2.1 table-driven 形状表

```go
// test/p2/compilability_inject/f1_vararg_test.go
package compilability_inject_test

import (
    "testing"
    "github.com/<...>/wangshu"
    "github.com/<...>/wangshu/internal/bridge"
)

func TestF1VarargInject(t *testing.T) {
    cases := []struct {
        name string
        src  string
        want bridge.Compilability   // 全部期望 NotCompilable
    }{
        {
            name: "fully vararg formal",
            src:  `local function f(...) return select("#", ...) end; return f`,
            want: bridge.CompNotCompilable,
        },
        {
            name: "partial vararg",
            src:  `local function f(a, b, ...) return a + b end; return f`,
            want: bridge.CompNotCompilable,
        },
        {
            name: "vararg expr in body",
            src:  `local f = function(...) local x = ...; return x end; return f`,
            want: bridge.CompNotCompilable,
        },
        {
            name: "main chunk implicit vararg",
            src:  `return 42`,                        // 主 chunk 永远 vararg(03 §3.1.5)
            want: bridge.CompNotCompilable,
        },
        {
            name: "no vararg pure arith",
            src:  `local function f(a,b) return a*b+1 end; return f`,
            want: bridge.CompCompilable,             // 反向用例:必须 Compilable
        },
    }

    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            prog, err := wangshu.Compile([]byte(tc.src), tc.name)
            if err != nil { t.Fatal(err) }
            // 取主 chunk 之后的子 Proto 判定(主 chunk 总是 NotCompilable)
            proto := prog.HotProto()                  // 测试 helper:取脚本返回的子 Proto
            got := bridge.CompilabilityOf(proto)
            if got != tc.want {
                t.Fatalf("compilability mismatch: got %v want %v", got, tc.want)
            }
        })
    }
}
```

#### 2.2.2 注入 fuzz 子测

```go
// 注入器:随机生成 [含/不含 vararg 形状] 的 Lua 脚本,断言判定与三重检查一致
func FuzzF1VarargInjection(f *testing.F) {
    f.Add(uint32(0xdeadbeef))
    f.Fuzz(func(t *testing.T, seed uint32) {
        // 用 seed 生成两组脚本:含 vararg 与不含
        srcVararg := genFuncWithVararg(seed)         // 注入器(随机插入 vararg 形态)
        srcPlain  := genFuncPlain(seed)              // 同 seed 生成纯算术脚本

        // 三重检查同时通过:AST.IsVararg / Proto.IsVararg / VARARG opcode
        progV, _ := wangshu.Compile([]byte(srcVararg), "v")
        progP, _ := wangshu.Compile([]byte(srcPlain),  "p")
        protoV  := progV.HotProto()
        protoP  := progP.HotProto()

        if bridge.CompilabilityOf(protoV) != bridge.CompNotCompilable {
            t.Errorf("[FALSE NEGATIVE] vararg inject not detected:\n%s", srcVararg)
        }
        if bridge.CompilabilityOf(protoP) != bridge.CompCompilable {
            // 反向断言:无 vararg 的脚本不应被误判
            // (允许其他 F2-F7 触发,但若仅 F1 触发就是错的)
            if reasonsOf(protoP) & reasonVararg != 0 {
                t.Errorf("[FALSE POSITIVE] plain script falsely flagged vararg:\n%s", srcPlain)
            }
        }
    })
}
```

> **关键设计**:**「假阴性」(漏报误判)是主防线**——`CompNotCompilable` 没出现就是事故。**「假阳性」(过保守)只检查仅 F1 触发是否存在**——其他 Fk 触发是允许的(漏判可接受,§1.1 V1)。

#### 2.2.3 注入器实现

```go
// genFuncWithVararg 按 seed 生成必含 vararg 形状的脚本
func genFuncWithVararg(seed uint32) string {
    rng := newRNG(seed)
    forms := []string{
        "local function f(...) return ... end; return f",
        "local function f(a, ...) return a + select('#', ...) end; return f",
        "local f = function(...) local x = ...; return x end; return f",
        "return function(...) return ... end",
    }
    // 加 N 条不影响 vararg 判定的「噪声」语句
    noise := genNoise(rng, 5+rng.Intn(20))
    return noise + "\n" + forms[int(rng.Uint32()) % len(forms)]
}

// genFuncPlain 按同 seed 生成纯算术(无 vararg)脚本
func genFuncPlain(seed uint32) string {
    rng := newRNG(seed)
    return "local function f(a,b) return a*b + " +
        intLit(rng) + " end; return f"
}
```

### 2.3 F2 协程注入测试

#### 2.3.1 table-driven 表

```go
// f2_coroutine_test.go
func TestF2CoroutineInject(t *testing.T) {
    cases := []struct {
        name   string
        src    string
        want   bridge.Compilability
        reason reasonsBitmap                          // 期望的 reason 位
    }{
        // (1) 直接调用 coroutine.* —— §3.2.4 模式 (1)
        {"direct yield",   `local function f() coroutine.yield(1) end; return f`,
            bridge.CompNotCompilable, reasonYield | reasonCoroutine},
        {"direct resume",  `local function f(c) coroutine.resume(c) end; return f`,
            bridge.CompNotCompilable, reasonResume | reasonCoroutine},
        {"direct wrap",    `local function f() coroutine.wrap(g)() end; return f`,
            bridge.CompNotCompilable, reasonCoroutine | reasonUnknownCall},

        // (2) 局部别名 —— §3.2.4 模式 (2),P2 初版通过 callsUnknownFn 兜底
        {"local alias",    `local y = coroutine.yield; local function f() y(1) end; return f`,
            bridge.CompNotCompilable, reasonUnknownCall},

        // (3) 表别名 —— §3.2.4 模式 (3),通过 callsUnknownFn 兜底
        {"table alias",    `local co = require("coroutine"); local function f() co.yield() end; return f`,
            bridge.CompNotCompilable, reasonUnknownCall},

        // (4) 经传入参数 —— §3.2.4 模式 (4),通过 callsUnknownFn 兜底
        {"param passthru", `local function f(y, x) y(x) end; return f`,
            bridge.CompNotCompilable, reasonUnknownCall},

        // 反向:无任何调用的纯算术
        {"pure arith no call", `local function f(a,b) return a*b+1 end; return f`,
            bridge.CompCompilable, 0},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            prog, _ := wangshu.Compile([]byte(tc.src), tc.name)
            proto := prog.HotProto()
            if got := bridge.CompilabilityOf(proto); got != tc.want {
                t.Fatalf("got %v want %v (reason=%v)", got, tc.want, reasonsOf(proto))
            }
        })
    }
}
```

#### 2.3.2 Fuzz:间接调用全保守

```go
// FuzzF2UnknownCallConservative —— P2 初版 isKnownLocalCall 全返 false,
// 所以「任何调用」都应触发 callsUnknownFn,导致 NotCompilable。
// 这一 fuzz 验证「绝大多数有外部调用的函数都判不可编译」(§3.2.7)的工程纪律。
func FuzzF2UnknownCallConservative(f *testing.F) {
    f.Add(uint32(0x1234))
    f.Fuzz(func(t *testing.T, seed uint32) {
        src := genFuncWithCall(seed)                 // 必含一处调用
        prog, _ := wangshu.Compile([]byte(src), "f2-fuzz")
        proto := prog.HotProto()
        if bridge.CompilabilityOf(proto) != bridge.CompNotCompilable {
            t.Errorf("[FALSE NEGATIVE] call-bearing function not flagged: %s", src)
        }
    })
}
```

### 2.4 F3/F4 debug/setfenv 注入测试

合并讨论(两形状识别方式同构):

```go
func TestF3F4DebugSetfenvInject(t *testing.T) {
    cases := []struct {
        name string
        src  string
        want bridge.Compilability
    }{
        // F3 debug
        {"debug.traceback in body", `local function f() return debug.traceback() end; return f`,
            bridge.CompNotCompilable},
        {"debug.getlocal",          `local function f() return debug.getlocal(1, 1) end; return f`,
            bridge.CompNotCompilable},
        {"debug aliased to local",  `local d = debug; local function f() return d.traceback() end; return f`,
            bridge.CompNotCompilable},                // NameExpr{"debug"} 即触发,无 scope 分析(03 §3.3.3)

        // F4 setfenv
        {"setfenv direct",   `local function f(t) setfenv(1, t) end; return f`,
            bridge.CompNotCompilable},
        {"getfenv direct",   `local function f() return getfenv(2) end; return f`,
            bridge.CompNotCompilable},

        // 反向
        {"no debug no setfenv", `local function f(a,b) return a+b end; return f`,
            bridge.CompCompilable},
    }
    // ... 运行表 ...
}
```

> **F3 已知假阳性**:用户局部 `local debug = {}` 也会被误标(03 §3.3.3 接受)。本测试 **不为这种边角加用例**——避免给「scope-aware 名字解析」(03 §9 GAP-5) 设错误的回归基准。

### 2.5 F5 过大函数边界值测试

```go
func TestF5OverSizeBoundary(t *testing.T) {
    cases := []struct {
        name  string
        insns int
        regs  int
        want  bridge.Compilability
    }{
        {"insns 1999",  1999, 50, bridge.CompCompilable},
        {"insns 2000",  2000, 50, bridge.CompCompilable},     // ≤ 阈值,通过
        {"insns 2001",  2001, 50, bridge.CompNotCompilable},  // > 阈值,触发
        {"regs 199",    100,  199, bridge.CompCompilable},
        {"regs 200",    100,  200, bridge.CompCompilable},
        {"regs 201",    100,  201, bridge.CompNotCompilable},
        {"both over",   3000, 250, bridge.CompNotCompilable},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            // 用代码生成器构造指定指令数 / 寄存器数的 Proto
            src := genSizedFunc(tc.insns, tc.regs)
            prog, _ := wangshu.Compile([]byte(src), tc.name)
            proto := prog.HotProto()
            if got := bridge.CompilabilityOf(proto); got != tc.want {
                t.Fatalf("got %v want %v (insns=%d regs=%d)",
                    got, tc.want, len(proto.Code), proto.MaxStack)
            }
        })
    }
}

// genSizedFunc 按目标 insns / regs 构造 Lua 源(连续 local 累加 + 大寄存器栈)
func genSizedFunc(targetInsns, targetRegs int) string {
    var b strings.Builder
    b.WriteString("local function f()\n")
    // 用 local x = a + b 逐句累加,产生约 targetInsns 条指令
    for i := 0; i < targetInsns/3; i++ {
        fmt.Fprintf(&b, "  local x%d = %d + %d\n", i, i, i+1)
    }
    // 撑大寄存器:深嵌 do ... end
    b.WriteString("end\nreturn f\n")
    return b.String()
}
```

### 2.6 F6 嵌套深度 / upvalue 边界

```go
func TestF6ClosureBoundary(t *testing.T) {
    cases := []struct{
        name  string
        src   string
        want  bridge.Compilability
    }{
        {"depth 3 ok",      makeNestedFunc(3), bridge.CompCompilable},     // ≤ 3
        {"depth 4 fail",    makeNestedFunc(4), bridge.CompNotCompilable},  // > 3
        {"upval 8 ok",      makeUpvalFunc(8), bridge.CompCompilable},
        {"upval 9 fail",    makeUpvalFunc(9), bridge.CompNotCompilable},
    }
    // ... 运行表 ...
}

// makeNestedFunc 生成深度为 d 的嵌套函数
func makeNestedFunc(d int) string {
    var b strings.Builder
    for i := 0; i < d; i++ { b.WriteString("function() ") }
    b.WriteString("return 42 ")
    for i := 0; i < d; i++ { b.WriteString("end ") }
    return "return " + b.String()
}
```

### 2.7 F7 P3 后端能力查询测试

```go
// 用 mock P3 控制 SupportsAllOpcodes 返回值
func TestF7BackendCapability(t *testing.T) {
    cases := []struct{
        name        string
        supportFn   func(*bytecode.Proto) bool
        wantInProto bridge.Compilability
    }{
        {"backend supports all",    func(p *bytecode.Proto) bool { return true },  bridge.CompCompilable},
        {"backend supports none",   func(p *bytecode.Proto) bool { return false }, bridge.CompNotCompilable},
        {"backend rejects vararg",
            func(p *bytecode.Proto) bool { return !p.IsVararg },
            bridge.CompNotCompilable,                // 主 chunk 是 vararg → 拒
        },
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            mockP3 := &mockP3Compiler{supportFn: tc.supportFn}
            b := bridge.New(mockP3)
            // 跑可编译性分析
            prog, _ := compileWithBridge(`local function f(a,b) return a+b end; return f`, b)
            proto := prog.HotProto()
            if got := bridge.CompilabilityOf(proto); got != tc.wantInProto {
                t.Fatalf("got %v want %v", got, tc.wantInProto)
            }
        })
    }
}
```

### 2.8 综合注入 fuzz(inject_fuzz_test.go)—— V7 收口

```go
// FuzzCombinedInject —— 主防线 fuzz:随机选 F1-F7 子集注入,断言任一注入即 NotCompilable
func FuzzCombinedInject(f *testing.F) {
    f.Add(uint32(0xc0ffee))
    f.Fuzz(func(t *testing.T, seed uint32) {
        rng := newRNG(seed)
        // 随机选注入子集(7 位)
        injectMask := rng.Uint32() & 0x7f             // F1..F7
        src := genScriptWithInjection(rng, injectMask)
        prog, err := wangshu.Compile([]byte(src), "combined")
        if err != nil { return }                      // 编译失败的脚本跳过(parse 错不归本测)

        proto := prog.HotProto()
        got := bridge.CompilabilityOf(proto)
        if injectMask != 0 && got != bridge.CompNotCompilable {
            t.Errorf("[FALSE NEGATIVE] injectMask=0x%x not detected:\n%s", injectMask, src)
        }
        if injectMask == 0 && got == bridge.CompNotCompilable {
            // 注入空集时若仍判 NotCompilable,说明 noise 自身触发了某 Fk
            // (允许,因为 noise 可能恰好引入了某个形状——降级为 INFO,不算事故)
            t.Logf("[INFO] no inject but flagged: reasons=0x%x src:\n%s",
                reasonsOf(proto), src)
        }
    })
}
```

**fuzz 预算**:CI 上 `go test -fuzz=FuzzCombinedInject -fuzztime=10m`,本地 1e6 次。**任一假阴性即视为 P2 主防线失守**——立即停 PR,根因分析,补 reproducer 入 table-driven 表(回归测试)。

### 2.9 覆盖率指标(GAP-1)

**当前缺口**:fuzz 输入空间巨大(F1-F7 七个独立维度 × 内部形态 × noise),如何度量「fuzz 跑得够不够」?候选:

- **opcode 覆盖**:跑过 fuzz 后,`Proto.Code` 中的 opcode 0..37 是否全部被注入器生成过 → 简单实现,但与「F1-F7 形状覆盖」错位
- **形状覆盖**:F1-F7 每条触发的 reason 位至少被生成 N 次 → 对齐 P2 验收,推荐
- **覆盖引导(coverage-guided)**:Go 1.18+ fuzz 引擎自动覆盖引导,本身已经做这件事

**P2 初版**:不引入显式覆盖度量,依赖 Go 1.18+ fuzz 的内置覆盖引导。**实测发现某条 Fk 漏注入再加显式度量**(记 §11 缺口)。

---

## 3. 计数与阈值测试(V8-V10)

### 3.1 V8 计数累积合理

```go
// test/p2/profiling/accumulate_test.go
func TestProfileAccumulate(t *testing.T) {
    // 用一个含 FORLOOP 的脚本,跑 N 次后断言 backEdge[pc] == N
    src := `
local function loop(n)
    local s = 0
    for i = 1, n do s = s + i end       -- FORLOOP 回边
    return s
end
return loop`

    prog, _ := wangshu.Compile([]byte(src), "accumulate")
    state := wangshu.NewState(nil)

    // 第一次跑 100 次回边
    state.Call(prog, 100)
    proto := prog.HotProto()
    pd := bridge.ProfileDataOf(state, proto)
    forloopPC := findFORLOOPPC(proto)                 // 测试 helper:扫 Code 找 FORLOOP 位置
    if pd.BackEdge[forloopPC] != 100 {
        t.Errorf("after 100 iters: got backEdge=%d want 100", pd.BackEdge[forloopPC])
    }

    // 第二次再跑 50 次,累积到 150
    state.Call(prog, 50)
    if pd.BackEdge[forloopPC] != 150 {
        t.Errorf("after +50 iters: got backEdge=%d want 150", pd.BackEdge[forloopPC])
    }

    // 入口计数:每次 Call 一次
    if pd.EntryCount != 2 {
        t.Errorf("entry count: got %d want 2", pd.EntryCount)
    }
}

// V8 子测:TierStuck 后停止累积
func TestProfileStuckStops(t *testing.T) {
    // 注入 F2(yield)使 considerPromotion 立即转 Stuck
    src := `local function f() coroutine.yield() end; return f`
    prog, _ := wangshu.Compile([]byte(src), "stuck")
    state := wangshu.NewState(nil)

    // 第一次 Call:触发 considerPromotion,转 Stuck
    state.Call(prog)
    proto := prog.HotProto()
    pd := bridge.ProfileDataOf(state, proto)
    if pd.TierState != bridge.TierStuck {
        t.Fatalf("expect TierStuck got %v", pd.TierState)
    }
    entryAfter1 := pd.EntryCount

    // 后续 Call:onEnter 守卫拦下,EntryCount 不增(详见 [01 §4.2] 守卫)
    state.Call(prog)
    state.Call(prog)
    if pd.EntryCount != entryAfter1 {
        t.Errorf("EntryCount changed after Stuck: %d → %d", entryAfter1, pd.EntryCount)
    }
}
```

### 3.2 V9 阈值边界值

```go
// test/p2/profiling/threshold_test.go
//
// 边界值:HotBackEdgeThreshold=1000
//   - 999  次回边 → considerPromotion 不调用
//   - 1000 次回边 → 调用 1 次(`>=` 语义,01 §5.3.2)
//   - 1001 次回边 → 仍只 1 次(幂等,V13)
//   - 2000 次回边 → 仍只 1 次

func TestPromotionThreshold(t *testing.T) {
    cases := []struct{
        name        string
        iters       int
        wantCallCnt int
    }{
        {"under 999",    999,  0},
        {"at 1000",      1000, 1},                    // 边界触发
        {"over 1001",    1001, 1},                    // 幂等
        {"way over 2000", 2000, 1},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            // mock considerPromotion 注入计数器
            mock := &mockBridge{}
            src := loopOf(tc.iters)
            runWithBridge(src, mock)
            if mock.consideredCount != tc.wantCallCnt {
                t.Errorf("considerPromotion called %d times, want %d",
                    mock.consideredCount, tc.wantCallCnt)
            }
        })
    }
}
```

### 3.3 V10 profile 开关切换 byte-equal

V10 是 P2 的**核心承诺**之一:**P2 启用与否不改 P1 输出**。

```go
// test/p2/profiling/byte_equal_test.go
func TestProfileEnabledByteEqual(t *testing.T) {
    // 三档脚本 + benchmark game 五项 + boundary mini(承 P1 [12 §6])
    scripts := loadDifftestScripts()                  // 复用 P1 差分套脚本

    for _, script := range scripts {
        t.Run(script.Name, func(t *testing.T) {
            // 路径 1:profileEnabled=false (P1-only 行为)
            outP1 := runWithProfile(script.Source, false)

            // 路径 2:profileEnabled=true (P2 启用,但 mock P3 永远拒)
            outP2 := runWithProfile(script.Source, true)

            if !bytes.Equal(outP1, outP2) {
                t.Errorf("profile toggle byte mismatch:\nlen(P1)=%d len(P2)=%d\ndiff at offset %d",
                    len(outP1), len(outP2), firstDiff(outP1, outP2))
            }
        })
    }
}

// runWithProfile 用指定 profileEnabled 跑同一脚本
func runWithProfile(src []byte, enabled bool) []byte {
    cfg := bridge.Config{ProfileEnabled: enabled, P3: &alwaysRejectP3{}}
    state := wangshu.NewStateWithBridge(cfg)
    prog, _ := wangshu.Compile(src, "byteEqual")
    var buf bytes.Buffer
    state.SetStdout(&buf)
    state.Call(prog)
    return buf.Bytes()
}
```

> **alwaysRejectP3**:mock P3 实现 `SupportsAllOpcodes` 永返 false 且 `Compile` 永返错误。这让 P2 启用但**不发生升层**,确保差分基准是「P1 解释器 + 计数」vs 「P1 解释器」,不混入 P3 行为。

---

## 4. 状态机不变量测试(V11-V13)

### 4.1 V11 状态机单向(table-driven)

```go
// test/p2/tierstate/transition_test.go
//
// 状态机定义(承 [04-try-compile-fallback] §2):
//   合法转移:Interp→Gibbous, Interp→Stuck
//   非法转移:Gibbous→*, Stuck→*, *→Interp(任何回边)
//
// 列举所有 (from, to) 对,断言:
//   - 合法对:setTierState 后 pd.tierState == to
//   - 非法对:setTierState panic("illegal tier transition: ...")

func TestTierStateUnidirectional(t *testing.T) {
    cases := []struct {
        from, to bridge.TierState
        legal    bool
    }{
        {bridge.TierInterp,  bridge.TierGibbous, true},
        {bridge.TierInterp,  bridge.TierStuck,   true},
        {bridge.TierInterp,  bridge.TierInterp,  false},   // self-loop 不允许
        {bridge.TierGibbous, bridge.TierInterp,  false},   // 关键:Gibbous→Interp 是 deopt 形态,P2 禁止
        {bridge.TierGibbous, bridge.TierStuck,   false},
        {bridge.TierGibbous, bridge.TierGibbous, false},
        {bridge.TierStuck,   bridge.TierInterp,  false},
        {bridge.TierStuck,   bridge.TierGibbous, false},
        {bridge.TierStuck,   bridge.TierStuck,   false},
    }

    for _, tc := range cases {
        name := fmt.Sprintf("%v_to_%v", tc.from, tc.to)
        t.Run(name, func(t *testing.T) {
            pd := &bridge.ProfileData{TierState: tc.from}
            err := bridge.TrySetTierState(pd, tc.to)      // 测试 helper(实际由 04 提供)
            if tc.legal && err != nil {
                t.Errorf("legal transition rejected: %v", err)
            }
            if !tc.legal && err == nil {
                t.Errorf("illegal transition accepted: %v→%v", tc.from, tc.to)
            }
        })
    }
}
```

### 4.2 V12 TierStuck 不重试

```go
// test/p2/tierstate/stuck_no_retry_test.go
func TestStuckNoRetry(t *testing.T) {
    // 注入 F2 使 considerPromotion 立即转 Stuck
    src := `local function f() coroutine.yield() end; for i=1,10000 do f() end`
    mock := &mockBridge{}
    runWithBridge(src, mock)

    if mock.consideredCount != 1 {
        t.Errorf("expected exactly 1 considerPromotion call, got %d", mock.consideredCount)
    }
    // 即便后续上万次 onEnter,considerPromotion 不再被调
    // (TierInterp 守卫在 [01 §4.1/§4.2] 拦下)
}
```

### 4.3 V13 considerPromotion 幂等

```go
// 直接调 considerPromotion 二次,断言第二次 no-op
func TestConsiderPromotionIdempotent(t *testing.T) {
    pd := &bridge.ProfileData{TierState: bridge.TierInterp, Compilable: bridge.CompCompilable}
    proto := makeTestProto()

    mockP3 := &countingMockP3{}
    b := bridge.New(mockP3)

    b.ConsiderPromotion(proto, pd)                    // 第一次:升 Gibbous
    if pd.TierState != bridge.TierGibbous {
        t.Fatalf("expect Gibbous after first call, got %v", pd.TierState)
    }
    if mockP3.compileCount != 1 {
        t.Fatalf("expect 1 compile, got %d", mockP3.compileCount)
    }

    b.ConsiderPromotion(proto, pd)                    // 第二次:守卫拦下,no-op
    b.ConsiderPromotion(proto, pd)                    // 第三次:同
    if mockP3.compileCount != 1 {
        t.Errorf("idempotent broken: compile called %d times", mockP3.compileCount)
    }
}
```

---

## 5. 跨 State 并发测试(V14-V16)

### 5.1 V14 `-race` 通过

```go
// test/p2/concurrency/race_test.go
//
// 跑法:go test -race -count=10 ./test/p2/concurrency/
// 任一 race 警告即破。

func TestProfileTableRace(t *testing.T) {
    src := `
local function loop(n)
    local s = 0
    for i = 1, n do s = s + i end
    return s
end
return loop`

    prog, _ := wangshu.Compile([]byte(src), "race")
    var wg sync.WaitGroup
    workers := runtime.NumCPU() * 2
    iters := 1000

    for i := 0; i < workers; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            // 每 goroutine 独立 State,profileTable 私有(承 [01 §6.3])
            state := wangshu.NewState(nil)
            for j := 0; j < iters; j++ {
                state.Call(prog, 100)
            }
        }()
    }
    wg.Wait()
    // 跑过即过(`-race` 跑时若有共享数据写,Go runtime 会报 race)
}
```

### 5.2 V15 多 State 累积独立

```go
// test/p2/concurrency/multi_state_test.go
func TestMultiStateAccumulationIndependent(t *testing.T) {
    src := `local function f(n) local s=0; for i=1,n do s=s+i end; return s end; return f`
    prog, _ := wangshu.Compile([]byte(src), "indep")
    proto := prog.HotProto()

    // 两个独立 State,各自跑 N 次
    s1 := wangshu.NewState(nil); s1.Call(prog, 100)
    s2 := wangshu.NewState(nil); s2.Call(prog, 50)

    pd1 := bridge.ProfileDataOf(s1, proto)
    pd2 := bridge.ProfileDataOf(s2, proto)
    forloopPC := findFORLOOPPC(proto)

    // V15:两 State 累积独立,无跨共享
    if pd1.BackEdge[forloopPC] != 100 {
        t.Errorf("s1 backEdge: got %d want 100", pd1.BackEdge[forloopPC])
    }
    if pd2.BackEdge[forloopPC] != 50 {
        t.Errorf("s2 backEdge: got %d want 50", pd2.BackEdge[forloopPC])
    }
}
```

### 5.3 V16 sync.Pool 复用

```go
// test/p2/concurrency/pool_reuse_test.go
//
// (B) 方案行为锁定:State 经 Pool Reset/Get,profileTable 在 Reset 后清空
// 这是 [01 §6.4.2] 的「已知限制」——固化为测试,防被静默破

func TestPoolReuseClears(t *testing.T) {
    pool := &sync.Pool{
        New: func() interface{} { return wangshu.NewState(nil) },
    }
    src := `local function f(n) for i=1,n do end end; return f`
    prog, _ := wangshu.Compile([]byte(src), "pool")
    proto := prog.HotProto()

    // 第一次 Get,跑 100 次,profileTable 累积 100
    s1 := pool.Get().(*wangshu.State)
    s1.Call(prog, 100)
    pd1 := bridge.ProfileDataOf(s1, proto)
    forloopPC := findFORLOOPPC(proto)
    if pd1.BackEdge[forloopPC] != 100 {
        t.Fatalf("first call: got %d", pd1.BackEdge[forloopPC])
    }

    // Reset 后归还 Pool
    s1.Reset()
    pool.Put(s1)

    // 第二次 Get,profileTable 应空(B 方案行为)
    s2 := pool.Get().(*wangshu.State)
    pd2 := bridge.ProfileDataOf(s2, proto)
    if pd2 != nil && pd2.BackEdge != nil && pd2.BackEdge[forloopPC] != 0 {
        t.Errorf("after Reset: backEdge should be 0, got %d", pd2.BackEdge[forloopPC])
    }
}
```

> **(C) 启用前后的测试切换**:本测试目前锁定 (B) 方案行为(Reset 全清);(C) 双表混合方案启用时,本测试需调整为「私有表清空但 Proto 旁聚合表持有累积」。详见 [01 §6.4.1](./01-profiling.md) 与本文 §11 缺口。

---

## 6. 升层日志断言(V17-V19)

### 6.1 V17 三类日志格式逐字节匹配

[04-try-compile-fallback](./04-try-compile-fallback.md) §6 定稿三类升层日志格式:

```
function <name> promoted to gibbous
function <name> stays interpreted (not compilable: <reason>)
function <name> compile failed, stays interpreted: <err>
```

测试断言:**逐字节匹配**(format string 的稳定性是诊断契约)。

```go
// test/p2/promotion_log/log_format_test.go
func TestPromotionLogFormat(t *testing.T) {
    cases := []struct {
        name      string
        scenario  func(*bridge.Bridge, *bytecode.Proto)   // 触发升层路径
        wantRegex string
    }{
        {
            name: "promoted",
            scenario: func(b *bridge.Bridge, p *bytecode.Proto) {
                // 注入热度 + Compilable + 编译成功 → 升 Gibbous
                triggerHotPromotionSuccess(b, p)
            },
            wantRegex: `^function \S+ promoted to gibbous$`,
        },
        {
            name: "stays not compilable",
            scenario: func(b *bridge.Bridge, p *bytecode.Proto) {
                // F2 注入 → CompNotCompilable → Stuck
                triggerNotCompilable(b, p)
            },
            wantRegex: `^function \S+ stays interpreted \(not compilable: \S+\)$`,
        },
        {
            name: "compile failed",
            scenario: func(b *bridge.Bridge, p *bytecode.Proto) {
                // CompCompilable + mock P3 编译失败 → Stuck
                triggerCompileFail(b, p)
            },
            wantRegex: `^function \S+ compile failed, stays interpreted: .+$`,
        },
    }

    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            var buf bytes.Buffer
            diag := newDiag(&buf)                     // 抓 diag 输出
            b := bridge.NewWithDiag(diag, &mockP3{})
            proto := makeTestProto()
            tc.scenario(b, proto)

            line := strings.TrimSpace(buf.String())
            re := regexp.MustCompile(tc.wantRegex)
            if !re.MatchString(line) {
                t.Errorf("log mismatch:\nwant /%s/\ngot  %q", tc.wantRegex, line)
            }
        })
    }
}
```

### 6.2 V18 落点必须是 gibbous 非 bridge

[00-overview](./00-overview.md) §1 末:**「日志里不会有 `function promoted to bridge`;P2 触发的升层日志是 `function promoted to gibbous`」**。这是 P2 设计的核心反断言之一(bridge 不是执行层)。

```go
// test/p2/promotion_log/log_target_test.go
func TestNoPromotedToBridge(t *testing.T) {
    // 在所有 P2 测试运行的过程中,grep 全部 diag 输出
    // 任一含 `promoted to bridge` 即视为破

    var allDiagOutput bytes.Buffer
    diag := newDiag(&allDiagOutput)
    bridge.SetGlobalDiag(diag)
    defer bridge.SetGlobalDiag(nil)

    // 跑全 P2 套
    runAllP2Tests(t)

    output := allDiagOutput.String()
    if strings.Contains(output, "promoted to bridge") {
        t.Fatalf("found `promoted to bridge` in diag (must be `promoted to gibbous`):\n%s",
            extractMatchingLines(output, "promoted to bridge"))
    }
}
```

> **设计理由**(承 [00-overview](./00-overview.md) §1 末):bridge 是基建,不分配月相,不是执行层。日志写 `promoted to gibbous`(落到 P3 的 Wasm 层)是「P2 决策、P3 落地」分工的物理体现。

### 6.3 V19 诊断接口 race-safe

```go
// test/p2/promotion_log/diag_race_test.go
//
// 跑法:go test -race -count=10 ./test/p2/promotion_log/

func TestDiagRaceSafe(t *testing.T) {
    var buf bytes.Buffer
    diag := newDiag(&buf)
    bridge.SetGlobalDiag(diag)
    defer bridge.SetGlobalDiag(nil)

    var wg sync.WaitGroup
    for i := 0; i < 100; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            // 多 goroutine 并发触发升层(各自 State 各自 Proto)
            triggerPromotion()
        }()
    }
    wg.Wait()
    // `-race` 跑过即过
}
```

---

## 7. 跨阶段差分(V20-V22)—— 核心承诺

### 7.1 V20 crescent-only vs P2-on-crescent byte-equal

V20 是 P2 设计的核心承诺:**P2 上线前后跑同一 Lua 脚本输出 byte-equal**(P2 不加速也不影响输出)。

任何破坏 byte-equal 的 P2 改动都直接判否——这与 P1 [12 §3](../p1-interpreter/12-testing-difftest.md) 的 byte-equal 差分主防线**同地位**。

```go
// test/p2/cross_phase_diff/crescent_only_vs_p2_test.go
func TestCrescentOnlyVsP2OnCrescent(t *testing.T) {
    // 复用 P1 差分套的全部脚本(承 P1 [12 §6])
    scripts := loadAllDifftestScripts()
    // 包括:三档(empty / count1k / count10k)
    //       benchmark game 五项(nbody / fasta / mandelbrot / spectral-norm / binary-trees)
    //       boundary mini(数字边界 / 字符串边界 / 表边界)

    for _, script := range scripts {
        t.Run(script.Name, func(t *testing.T) {
            // 路径 A:crescent-only(模拟 P1-only 部署,profileEnabled=false)
            outA := runCrescentOnly(script.Source)

            // 路径 B:P2-on-crescent(P2 启用 + 计数 + 升层尝试,但 mock P3 强制全 Stuck)
            outB := runP2OnCrescent(script.Source)

            if !bytes.Equal(outA, outB) {
                t.Fatalf("byte mismatch in %s:\nlen(A)=%d len(B)=%d\nfirst diff at %d:\n  A: %q\n  B: %q",
                    script.Name, len(outA), len(outB), firstDiff(outA, outB),
                    snippetAround(outA, firstDiff(outA, outB)),
                    snippetAround(outB, firstDiff(outA, outB)))
            }
        })
    }
}
```

> **测试设计的精妙处**:V20 不依赖 P3 实现完整——mock P3 强制全 Stuck,所有 Proto 走 crescent 解释。这让 V20 在 PB7 阶段就能跑(不等 P3 完成),且锁定「P2 启用本身不影响输出」的承诺。**P3 上线后,V20 仍要过**(只是路径 B 改成「真 P3 + 升 Gibbous」,同输入仍 byte-equal——但那时由 P3 测试套主管,本测试保持 mock P3 形态)。

### 7.2 V21 mock P3 vs 真 P3 行为差异度量

mock P3 与真 P3 在「升层成功」分支上**应当**行为等价(同输入同输出)——但 mock P3 的 dummy 实现需要谨慎,确保它不引入虚假的 byte-equal(假阴性)。

mock P3 应支持两种工作形态:

| 形态 | SupportsAllOpcodes | Compile | 走向 |
|---|---|---|---|
| **mockP3 reject all** | 永返 false | 永返错误 | 所有 Proto 走 Stuck,完全 crescent 解释 |
| **mockP3 dummy compile** | 永返 true(对纯算术 Proto) | 返 dummy `*GibbousCode`(实际仍走 crescent 解释) | Proto 升 Gibbous 但执行仍是解释器(dummy 假装编译) |

V21 测试:这两形态 + P1-only 三路径在差分套上输出 byte-equal。

```go
// test/p2/cross_phase_diff/mock_p3_vs_real_p3_test.go
func TestMockP3FormsByteEqual(t *testing.T) {
    scripts := loadCoreScripts()                      // 子集即可,不必全量
    for _, sc := range scripts {
        t.Run(sc.Name, func(t *testing.T) {
            outRej := runWithMockP3(sc.Source, &mockP3RejectAll{})
            outDmy := runWithMockP3(sc.Source, &mockP3DummyCompile{})
            outP1  := runP1Only(sc.Source)

            if !bytes.Equal(outRej, outP1) {
                t.Errorf("mockP3-reject vs P1: mismatch")
            }
            if !bytes.Equal(outDmy, outP1) {
                t.Errorf("mockP3-dummy vs P1: mismatch")
            }
        })
    }
}
```

> **当前缺口**:真 P3 上线前,mock P3 vs 真 P3 的「行为差异度量」无运行期参考点(mock 永远 byte-equal 于 P1,真 P3 byte-equal 于 mock 是 P3 测试的承诺,不是本文)。**P3 上线时跑联动测试**:把本文 V21 与 P3 §测试 的「真 P3 vs P1 byte-equal」联起来,形成传递闭环。记 §11 缺口。

### 7.3 V22 P1-only build tag 退化等价

V22 验证 P2 阶段独立交付的最强承诺:**`go build -tags '!profile'` 关掉 P2,望舒退化等价 P1**——`internal/bridge` 包不被引用,所有 P2 钩点(profileEnabled / onBackEdge / onEnter)在编译期消去。

```go
// test/p2/cross_phase_diff/build_tag_p1only_test.go
//
// 此测试在两种 build tag 下分别跑:
//   1. `go test -tags '!profile' ./test/p2/cross_phase_diff/`(P2 关闭)
//   2. `go test ./test/p2/cross_phase_diff/`(P2 启用)
// 通过 `//go:build` 控制条件编译。

//go:build !profile

package cross_phase_diff_test

func TestP1OnlyBuildEquivalent(t *testing.T) {
    // 此分支:bridge 包不存在,profileEnabled 永为 false
    // 跑 P1 差分套,应与「P2 启用但 mock P3 全 reject」路径 byte-equal
    scripts := loadAllDifftestScripts()
    p1onlyOut := make(map[string][]byte)
    for _, sc := range scripts {
        p1onlyOut[sc.Name] = runP1Only(sc.Source)
    }

    // 落盘成 golden file(本测试只产出 golden)
    writeGolden(t, "p1only_build_tag.golden", p1onlyOut)
}
```

```go
//go:build profile

package cross_phase_diff_test

func TestP1OnlyBuildEquivalent(t *testing.T) {
    // 此分支:P2 启用,跑差分套,应与 !profile 落盘的 golden 完全相等
    golden := readGolden(t, "p1only_build_tag.golden")
    scripts := loadAllDifftestScripts()
    for _, sc := range scripts {
        out := runWithP2(sc.Source, &mockP3RejectAll{})
        if !bytes.Equal(out, golden[sc.Name]) {
            t.Errorf("P2 path not equivalent to !profile build tag for %s", sc.Name)
        }
    }
}
```

**实现要点**:V22 用 `//go:build` 条件编译产出 golden(`!profile` 路径下),再在 `profile` 路径下读 golden 做对拍。这样**两种 build tag 下的等价性**变成可被 CI 跨次跑验证的事实。

> **CI 集成**:CI 跑两次 —— 一次 `go test -tags '!profile'`(产出 golden),一次 `go test`(对拍 golden)。两次都过才算 V22 验收通过。

---

## 8. PB7 端到端验收测试套清单

承 [00-overview](./00-overview.md) §4 PB7 验收(完成定义)的具体落地。**`make test-p2` 一键跑过全部 V1-V22**——这是 P2 验收的硬门槛。

### 8.1 Makefile 入口

```makefile
# 在主仓 Makefile 加入(P2 上线时):

.PHONY: test-p2 test-p2-fuzz test-p2-race test-p2-build-tag

# P2 主测试套(全部单测 + 短 fuzz)
test-p2:
	go test ./test/p2/...
	# 短 fuzz(每条 Fk 注入 fuzz 跑 30s)
	go test -fuzz=FuzzCombinedInject -fuzztime=30s ./test/p2/compilability_inject/

# 长 fuzz(夜间 / 发布前)
test-p2-fuzz:
	go test -fuzz=FuzzCombinedInject -fuzztime=10m ./test/p2/compilability_inject/
	go test -fuzz=FuzzF1VarargInjection -fuzztime=5m ./test/p2/compilability_inject/
	go test -fuzz=FuzzF2UnknownCallConservative -fuzztime=5m ./test/p2/compilability_inject/

# `-race` 套(并发不变量)
test-p2-race:
	go test -race -count=10 ./test/p2/concurrency/
	go test -race -count=10 ./test/p2/promotion_log/

# build tag 退化等价(V22)
test-p2-build-tag:
	# 第一次:!profile build,产 golden
	go test -tags '!profile' -run TestP1OnlyBuildEquivalent ./test/p2/cross_phase_diff/
	# 第二次:profile build,对拍 golden
	go test -run TestP1OnlyBuildEquivalent ./test/p2/cross_phase_diff/

# PB7 总验收:跑齐 V1-V22
test-p2-pb7: test-p2 test-p2-race test-p2-build-tag
	@echo "✓ PB7 acceptance: all V1-V22 verified"
```

### 8.2 PB7 验收的「过/不过」判据

按 [00-overview](./00-overview.md) §4 PB7 列出的四项:

| 验收项 | 对应口径 | 判据 |
|---|---|---|
| (a) 可编译性零误判 fuzz 通过 | V1-V7 | `make test-p2-fuzz` 长 fuzz 无假阴性 |
| (b) crescent-only 与 P2-on-crescent 跑 realworld 五脚本结果 byte-equal | V20 | `TestCrescentOnlyVsP2OnCrescent` 在差分套全脚本上过 |
| (c) 升层日志匹配预期 | V17-V19 | `TestPromotionLogFormat` + `TestNoPromotedToBridge` + `TestDiagRaceSafe` 全过 |
| (d) 多 State 并发 profileTable `-race` 通过 | V14 | `make test-p2-race` 跑过 |

四项**全过才算 PB7 验收**。任一项失败,PB7 不收单——P2 不交付。

### 8.3 测试套与 P2 各 PB 的对应

每条 PB 验收(00 §4)与本文测试的对应:

| PB | 内容 | 本文测试入口 |
|---|---|---|
| PB0 | bridge 包骨架 + ProfileData + profileTable | `byte_equal_test.go` 验「关 profileEnabled byte-equal」 |
| PB1 | 回边 / 入口采样点接入 | `accumulate_test.go` 验「计数累积非零」 |
| PB2 | IC 反馈聚合器 | (本文不主管,详见 [02-ic-feedback](./02-ic-feedback.md) 测试节) |
| PB3 | 静态可编译性分析器(F1-F7) | **`compilability_inject/` 全套**(本文 §2 主章) |
| PB4 | TierState 状态机 + considerPromotion + TierStuck 不重试 | `tierstate/` 全套(本文 §4) |
| PB5 | 升层日志格式 + 诊断接口 | `promotion_log/` 全套(本文 §6) |
| PB6 | P3/P4 接口实现 + mock P3 | `mock_p3_vs_real_p3_test.go`(本文 §7.2) |
| PB7 | 端到端验收 + 测试套 | **`make test-p2-pb7` 一键跑过**(本文 §8 主章) |

---

## 9. 与 P1 [12-testing-difftest] 的关系

### 9.1 两套测试并行不替换

[00-overview](./00-overview.md) §8 对照表已述,这里展开:

| 维度 | P1 [12-testing-difftest](../p1-interpreter/12-testing-difftest.md) | 本文 |
|---|---|---|
| 主防线 | byte-equal 差分(防投机错果) | 可编译性零误判注入(防安全闸门失守) |
| 验收口径形态 | 26 条「现象 → 决策」收口表 | 22 条「断言 → 检查路径」收口表 |
| 性能门 | ≥2x over gopher-lua 三档 + 五项 + boundary mini | 无 |
| 正确性门 | 三方差分 byte-equal + GC 透明性 + 措辞对齐 | 决策正确(F1-F7 零误判 + 累积合理 + 状态机不变量) |
| build tag | 单一 build(P1 实装) | 双 build(`!profile` 与 `profile`,V22) |

P2 上线后,**P1 [12 §10] 全部 26 条口径仍要在 `profile` build 下跑过**——P2 引入不豁免任何 P1 差分项。本文 §1 的 22 条是 P2 新增,**只在 `profile` build 下**被检查。

### 9.2 复用 P1 差分套的部分

本文 V10 / V20 / V21 / V22 都直接复用 P1 [12 §6](../p1-interpreter/12-testing-difftest.md) 的脚本资源:

- 三档(empty / count1k / count10k)
- benchmark game 五项(nbody / fasta / mandelbrot / spectral-norm / binary-trees)
- boundary mini(数字边界 / 字符串边界 / 表边界)

这些脚本是「真实负载形状」的最小公约——P1 用它们验性能 + byte-equal,P2 用它们验「P2 启用不破坏 byte-equal」。**测试资源共享,验收维度互补**。

### 9.3 「P2 启用」如何融入 P1 差分套

P1 的差分 fuzz([12 §3](../p1-interpreter/12-testing-difftest.md))在 P2 上线后扩展:

```
原 P1 差分:
  望舒(profileEnabled=false) vs gopher-lua vs 官方 lua5.1

P2 上线后扩展:
  望舒(profileEnabled=false) vs 望舒(profileEnabled=true, mock P3 reject all)
                                        ↑ 本文 V20 验
  望舒(profileEnabled=true)  vs gopher-lua vs 官方 lua5.1
                                        ↑ P1 [12 §3] 三方差分仍跑

P3 上线后再扩展:
  望舒(profileEnabled=true, real P3) vs gopher-lua vs 官方
                                        ↑ P3 §测试 主管(由 P3 文档定义)
```

P2 验收聚焦在第一行(「P2 启用与否的等价性」),不卷入第二/第三行(那是 P1 / P3 各自的差分主防线)。

---

## 10. 不变式清单(测试维度)

实现期与日常 review 用本表自检——**测试自身的不变式**:

| 不变式 | 含义 | 一旦违反的后果 |
|---|---|---|
| **T1 主防线零容忍** | V1-V7 任一假阴性即视为 P2 主防线失守,立即停 PR | 失守即可能误判 → P3 编译错误代码 → 静默错果 |
| **T2 byte-equal 不可破** | V20 / V22 任一脚本输出不同即破 P2 设计承诺 | 失守即破坏「P2 不影响 P1 输出」核心承诺 |
| **T3 测试与不变式一对一** | P2 各子文档(00-05)的每条不变式都有本文对应口径 | 失守即设计不变式无运行期验证(承诺空头) |
| **T4 mock P3 不引入假等价** | mock P3 的 dummy 实现不能让 byte-equal 「巧合」成立(必须真等价) | 失守即 V21 假阳性,真 P3 上线时暴雷 |
| **T5 build tag 切换不依赖默认值** | V22 在 `!profile` 与 `profile` 两 build 下都跑过 | 失守即 P1-only 部署可能引入 P2 残留 |
| **T6 race 套 `-count=10`** | V14 / V19 用 `-count=10` 而非 1,提高 race detector 命中率 | 失守即偶发 race 不被发现 |
| **T7 fuzz 长跑预算** | CI 短 fuzz(30s)+ 夜间长 fuzz(10m)同时存在,任一缺失视为 fuzz 不充分 | 失守即覆盖不足,主防线稀释 |
| **T8 失败用例固化** | fuzz 找出的任一失败用例须立即入 table-driven 表(回归测试) | 失守即同 bug 反复出现 |
| **T9 测试不依赖运行期阈值** | V8 / V9 用 mock 注入 considerPromotion,不依赖真实 1000 阈值的「调好后再过」 | 失守即阈值定标(01 §5.1)修改时大量测试连带失败 |
| **T10 P1 差分套不豁免** | P2 上线后 P1 [12 §10] 26 条口径在 `profile` build 下全跑过 | 失守即 P2 引入静默改变了 P1 行为 |

---

## 11. 文档缺口(待决)

| # | 缺口 | 触发条件 | 计划处理 |
|---|---|---|---|
| GAP-T1 | **可编译性 fuzz 的覆盖率指标**(§2.9) | 实测发现某条 Fk 漏注入(fuzz 1e6 次未触发某 reason 位) | 引入「形状覆盖度量」:跑过 fuzz 后断言 reasonsBitmap 各位至少触发 N 次 |
| GAP-T2 | **mock P3 与真 P3 行为差异如何度量**(§7.2) | 真 P3 上线前 mock 永远 byte-equal 于 P1,无差异参考点 | P3 上线时与 P3 §测试 联动:P3 测试主管「真 P3 byte-equal 于 P1-only」,本文 V21 主管「mock 形态间 byte-equal」 |
| GAP-T3 | **build tag 切换下 golden 的版本控制**(§7.3) | golden 文件随 P2 演进而变,需要 review 机制(防被静默改) | golden 入 git LFS 或显式签入 + diff review 强制 |
| GAP-T4 | **fuzz 输入合法性**(注入器生成的脚本需 parse 通过) | 注入器输出含语法错的脚本会被 wangshu.Compile 拒,fuzz 浪费 | 注入器加 `Lex+Parse` pre-check,只输出合法脚本(参考 P1 [12 §3.7](../p1-interpreter/12-testing-difftest.md) `isValidP1`) |
| GAP-T5 | **mockP3DummyCompile 的 dummy 实现**(§7.2) | dummy 是「假装编译,实际仍走解释」,但接口需要返回 `*GibbousCode` 类型 | 等 [05-p3-p4-interface](./05-p3-p4-interface.md) 定 GibbousCode 形态后,本文 §7.2 落实 mock |
| GAP-T6 | **PB7 验收的 CI 集成时序**(§8) | 短 fuzz / 长 fuzz / race 套 / build tag 套各有时间预算,CI 调度需排期 | 设计 CI workflow:PR check 跑短 fuzz + race + build tag(< 5 min);夜间跑长 fuzz(10m) |
| GAP-T7 | **真实负载脚本的 V20 覆盖**(§7.1) | 当前 V20 复用 P1 差分套,但首个宿主(规则脚本)的形态可能与 benchmark game 不同 | PB7 验收前补「首个宿主真实脚本」加入 V20 输入集 |

### 11.1 对上游(P2 各子文档)的回填请求

| 候选回填项 | 来源 | 状态 |
|---|---|---|
| `bridge.Bridge` 暴露 `ConsiderPromotion(proto, pd)` 公开方法(测试调用) | §4.3 | ⚠ 需 [04-try-compile-fallback](./04-try-compile-fallback.md) §3 在接口层暴露(测试不应碰 unexported) |
| `bridge.ProfileDataOf(state, proto)` 测试 helper | §3.1 / §5 | ⚠ 需 [01-profiling](./01-profiling.md) §6.5 加内部测试 helper(走 internal/testing/p2 包) |
| `bridge.TrySetTierState(pd, to)` 测试入口 | §4.1 | ⚠ 需 [04-try-compile-fallback](./04-try-compile-fallback.md) §2 暴露状态机转移函数(testing-only) |
| `mockP3RejectAll` / `mockP3DummyCompile` mock 实现 | §7 | ⚠ 需 [05-p3-p4-interface](./05-p3-p4-interface.md) §3 提供 mock 工厂 |
| `wangshu.NewStateWithBridge(cfg)` 测试入口(允许注入 mock P3) | §3.3 | ⚠ 需 [04-try-compile-fallback](./04-try-compile-fallback.md) 与 [11 §1](../p1-interpreter/11-embedding-arena-abi.md) 协商:测试用入口是否进公共 API,还是 internal/testing 包 |
| `bridge.SetGlobalDiag(diag)` 测试钩 | §6.2 | ⚠ 需 [04-try-compile-fallback](./04-try-compile-fallback.md) §6 升层日志接入 internal/diag 时同步暴露 |
| 主 chunk 调用后取「热」子 Proto 的 helper(`prog.HotProto()`) | §2.2 | ⚠ 需 P2 各文档的测试 helper 章节统一暴露 |

> **结论**:本文对 P2 各子文档的回填请求集中在「测试 helper 与 mock 工厂」——这是验收测试主文档的常态(测试需要「钩子」碰到 internal 状态)。**回填请求不影响公共 API**,只是 internal/testing 包或 testing-only 暴露。**实装时各 PB 一并落地**。

### 11.2 对 P1 上游文档的回填请求

无新增回填请求——P1 [12 §6](../p1-interpreter/12-testing-difftest.md) 的差分套脚本资源(三档 / 五项 / boundary mini)直接复用,无需 P1 改动。

---

## 12. 篇尾速查:V1-V22 一行总结

| # | 类目 | 一行断言 | 测试入口 |
|---|---|---|---|
| V1 | F1 vararg | `IsVararg` 三重一致 → `NotCompilable` | `f1_vararg_test.go` |
| V2 | F2 协程 | 任一 `coroutine.*` 或 unknown call → `NotCompilable` | `f2_coroutine_test.go` |
| V3 | F3 debug | `NameExpr{"debug"}` → `NotCompilable` | `f3_debug_test.go` |
| V4 | F4 setfenv | `NameExpr{"setfenv"/"getfenv"}` → `NotCompilable` | `f4_setfenv_test.go` |
| V5 | F5 过大 | `len(Code) > 2000` 或 `MaxStack > 200` → `NotCompilable` | `f5_oversize_test.go` |
| V6 | F6 嵌套 | 深度 > 3 或 upval > 8 → `NotCompilable` | `f6_closure_test.go` |
| V7 | F7 + 综合 | 后端不支持任一 opcode → `NotCompilable`;F1-F7 综合 fuzz | `f7_backend_test.go` + `inject_fuzz_test.go` |
| V8 | 计数累积 | `pd.backEdge[pc]` / `entryCount` 单调上升,Stuck 后停 | `accumulate_test.go` |
| V9 | 阈值生效 | `>= 1000` 才触发 considerPromotion | `threshold_test.go` |
| V10 | profile 切换 byte-equal | profileEnabled=false vs true 输出 byte-equal | `byte_equal_test.go` |
| V11 | 状态机单向 | 仅 Interp→Gibbous / Interp→Stuck,无回边 | `transition_test.go` |
| V12 | Stuck 不重试 | Stuck 后 considerPromotion callCount 不再增 | `stuck_no_retry_test.go` |
| V13 | considerPromotion 幂等 | 多次调用从第二次 no-op | `transition_test.go` |
| V14 | profileTable race-safe | `-race -count=10` 通过 | `race_test.go` |
| V15 | 多 State 累积独立 | 各 State 累积无跨共享 | `multi_state_test.go` |
| V16 | sync.Pool 复用 | Reset 后 profileTable 清空(B 方案) | `pool_reuse_test.go` |
| V17 | 升层日志格式 | 三类日志逐字节匹配 | `log_format_test.go` |
| V18 | 落点是 gibbous | grep `promoted to bridge` 必为空 | `log_target_test.go` |
| V19 | diag race-safe | `-race -count=10` 通过 | `diag_race_test.go` |
| V20 | crescent-only vs P2-on-crescent | byte-equal | `crescent_only_vs_p2_test.go` |
| V21 | mock P3 形态间 byte-equal | reject vs dummy vs P1-only 三路径 byte-equal | `mock_p3_vs_real_p3_test.go` |
| V22 | build tag 退化 | `!profile` 与 `profile` build 下 byte-equal | `build_tag_p1only_test.go` |

任一失败 → PB7 验收不收单 → P2 不交付。

---

相关:
[00-overview](./00-overview.md)(P2 总览 §4 PB7 验收 / §8 决策正确口径) ·
[01-profiling](./01-profiling.md)(计数 / 多 State 并发,V8-V10 / V14-V16 来源) ·
[02-ic-feedback](./02-ic-feedback.md)(IC feedback 聚合,本文不主管) ·
[03-compilability-analysis](./03-compilability-analysis.md)(F1-F7 安全闸门,V1-V7 来源) ·
[04-try-compile-fallback](./04-try-compile-fallback.md)(状态机 / 升层日志,V11-V13 / V17-V19 来源) ·
[05-p3-p4-interface](./05-p3-p4-interface.md)(mock P3 工厂,V21 来源) ·
[../p1-interpreter/12-testing-difftest](../p1-interpreter/12-testing-difftest.md)(P1 验收口径总表 §10 形态参考 / 差分套脚本资源复用) ·
[../p2-bridge/00-overview](./00-overview.md) §8(不变式清单,本文翻成验收断言) ·
[../roadmap.md](../roadmap.md) §5 原则 2(层间 byte-equal)+ 原则 3(每阶段独立交付,V22 来源) ·
[design-premises](../../../llmdoc/must/design-premises.md)(原则 4「fallback 不做完备性」)

