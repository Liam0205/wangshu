# P1:conformance 测试 + 层间差分 fuzz + 基准

> 状态:**设计阶段,可实现深度**。本文是**全 P1 文档集的「验收口径收口点」**:几乎每一篇 P1 文档
> 都把某些「这条行为差分时算不算一致 / 该不该逐字节比 / 措辞以谁为准」的口径问题**指向了 12**,
> 本文逐条收口(见 §10 验收口径总表——**本文存在的核心理由**)。它同时定义三套测试机制:
> conformance 套(对官方 Lua 5.1 语义)、层间逐字节差分 fuzz(roadmap §5 原则 2 的**主防线**)、
> 基准(性能验收 ≥2x over gopher-lua)。
>
> 上游契约:`docs/design/roadmap.md` (§1 校准测量 / §4 P1 验收「三档脚本 ≥2x、与 gopher-lua 差分
> 输出逐字节一致」/ §5 原则 2「层间逐字节差分是防投机错误静默错果的主防线,持续 fuzz」)、
> [architecture](../architecture.md) §4 不变式 2(**层间逐字节差分是 CI 必过门禁**)。
> 被收口的下游:[01](./01-value-object-model.md) §8、[02](./02-bytecode-isa.md) §10、
> [03](./03-frontend-lexer.md) §13、[04](./04-frontend-parser-codegen.md) §13、
> [05](./05-interpreter-loop.md) §2.2/§4.6/§13、[06](./06-memory-gc.md) §9.3/§11/§13、
> [07](./07-metatables-metamethods.md) §11、[09](./09-errors-pcall.md) §9.3、[10](./10-stdlib.md) §13。

对应目录:`test/conformance`(Lua 5.1 conformance 套,移植官方 + 自写)、
`test/difftest`(层间逐字节差分 fuzz harness)、`benchmarks/baseline`(校准测量数据与复现脚本)。
依赖被测对象 `internal/crescent`(解释器,语义 oracle)与公共门面 `wangshu`(Compile/Program/State)。

---

## 0. 本文在 P1 中的位置:验收口径的「最终法庭」

P1 的每篇文档都有一条共同的话术:**「语义对齐 Lua 5.1,任何疑义以参考实现为准,由差分测试钉死」**。
这把「正确性的最终判据」从「文档作者的判断」外移到「**一台官方 Lua 5.1 与一份 gopher-lua,跑同一脚本,
比对输出**」。本文就是这套外移判据的工程化:把「钉死」二字落成可运行的 harness、可入库的 golden、
可纳入 CI 的门禁。

三套机制的分工(金字塔自下而上,§1 给图):

| 机制 | 目录 | 判据 | 防的是什么 | roadmap 锚点 |
|---|---|---|---|---|
| **单元测试** | 各 `internal/*` 包内 `*_test.go` | 包内不变式(round-trip、边界) | 单元逻辑错误 | 构建顺序每步「单测通过」([architecture](../architecture.md) §5) |
| **conformance** | `test/conformance` | 脚本输出 == 官方 Lua 5.1 输出 | **语义错误**(与官方行为分叉) | §4「Lua 5.1 conformance 测试套」 |
| **差分 fuzz** | `test/difftest` | 望舒输出 == gopher-lua 输出 == 官方输出(逐字节,**随机脚本**) | **投机错误静默错果**(JIT 最危险 bug 类、GC 漏根、IC 失效漏) | §5 原则 2「层间逐字节差分主防线,持续 fuzz」 |
| **基准** | `benchmarks/baseline` | 三档脚本 ns/op ≤ gopher-lua / 2 | **性能不达标**(白做) | §4「三档脚本全部 ≥2x over gopher-lua」 |

**为什么 conformance 与差分 fuzz 是两件事**(常被混为一谈):
- conformance 是**固定的、人写的、有意覆盖语义角落**的用例集(像官方 `attrib.lua` 那样精心构造),用例数有限但**针对性强**,断言往往是「这段脚本应输出这几行」。它测「**我们知道该测什么**」。
- 差分 fuzz 是**随机生成海量脚本**,自己不知道正确答案,**靠第三方(官方/gopher-lua)当 oracle**判对错。它测「**我们没想到要测什么**」——投机错误、GC 时机依赖的偶发崩溃、IC 在某种罕见访问序下的失效,都是人写不出针对用例、只能靠随机量撞出来的。roadmap §5 原则 2 点名「**持续 fuzz**」正是因为这类 bug 无法用有限用例穷尽。

两者互补,缺一不可:conformance 给「已知语义」兜底,差分 fuzz 给「未知错误」撒网。

---

## 1. 测试金字塔总览

```
                         ┌───────────────────────────┐
                         │   benchmarks/baseline      │  性能验收:三档脚本 ≥2x over
                         │   (gopher-lua / LuaJ / JIT)│  gopher-lua,列内核形状(§8)
                         └───────────────────────────┘
                    ┌──────────────────────────────────────┐
                    │        test/difftest (fuzz)           │  主防线:随机脚本喂三 VM,
                    │  望舒 vs gopher-lua vs 官方 lua5.1     │  可观察输出逐字节一致(§3-§7)
                    │  + GC 压力 fuzz(高频 GC 透明性,§6)   │  CI 必过门禁(§9)
                    └──────────────────────────────────────┘
              ┌─────────────────────────────────────────────────┐
              │              test/conformance                    │  语义正确性:移植官方
              │  词法/语法/算术/表/元方法/协程/错误/闭包/stdlib  │  5.1 套 + 自写,输出对拍
              │  数值边界 ... (§2)                                │  官方 lua5.1(§2)
              └─────────────────────────────────────────────────┘
        ┌───────────────────────────────────────────────────────────┐
        │            单元测试(各 internal/* 包内)                   │  地基:arena round-trip、
        │  value 编解码 / arena 分配 / bytecode 编解码 / lex token   │  NaN 规范、寄存器分配黄金
        │  / parse AST / compile 寄存器分配黄金 / gc mark-sweep      │  测试、对拍 luac(§2.5)
        └───────────────────────────────────────────────────────────┘
```

金字塔形状的含义(与传统「单元多、e2e 少」一致,但本项目**差分 fuzz 是宽腰**而非窄顶——因为它是主防线):

- **底层最宽**:单元测试覆盖每个 `internal/*` 包,跟随 [architecture](../architecture.md) §5 构建顺序逐步建立(arena→value→object→bytecode→gc→lex→parse→compile→crescent),每步「可独立编译 + 单测通过」是进入下一步的前提。单元测试不依赖官方/gopher-lua,纯白盒。
- **conformance 中层**:用例数中等,**每条都有明确语义意图**,是「语义回归」的主体。
- **差分 fuzz 是异常宽的腰**:它不是金字塔尖的「少量 e2e」,而是**持续运行、海量随机**的主防线(roadmap §5 把它列为五条贯穿原则之一)。它的「宽」体现在**运行时长 × 随机种子数**,而非用例文件数。
- **基准在顶**:数量最少(三档脚本),但**验收权重最高**(不达 2x 则 P1 不成立)。

> **职责不重叠纪律**:同一个 bug 应优先被**更低层、更快、更可定位**的测试抓住。差分 fuzz 撞出 bug 后,应回写一条**确定性 conformance 用例**(把触发它的随机脚本最小化后入库),让回归不依赖再次随机撞中(§3.6 最小化)。这是「fuzz 撒网 → conformance 固化」的闭环。

---

## 2. conformance 测试套(`test/conformance`)

### 2.1 来源策略:移植官方 5.1 套 + 自写针对性用例

Lua 5.1 官方发行带一套测试脚本(`test/` 目录:`attrib.lua`/`calls.lua`/`closure.lua`/`constructs.lua`/`errors.lua`/`events.lua`/`math.lua`/`nextvar.lua`/`pm.lua`/`sort.lua`/`strings.lua`/`vararg.lua`/`api.lua`/`db.lua`/`gc.lua`/`literals.lua`/`code.lua`/`checktable.lua` 等)。这套是「事实标准 conformance 套」,但**直接全跑不可行**——它假设一个**完整的 5.1 runtime**(完整 `debug` 库、`package`/`require`、`os.execute`、`io` 全套、`loadfile`/`dofile` 读真实文件、`string.dump`/`loadstring` 字节码往返),而 P1 的 stdlib 是**裁剪过的**([10](./10-stdlib.md) §14 范围表)。

因此移植走**三分类策略**(每个官方测试文件标一个分类):

| 分类 | 判定 | 处理 |
|---|---|---|
| **可直接跑** | 只用 P1 已实现的 stdlib + 语言核心 | 原样纳入 `test/conformance/official/`,golden = 官方 lua5.1 输出 |
| **需裁剪后跑** | 主体可跑,少数段落依赖 P1 缺口(如 `db.lua` 大量用 `debug.getlocal`/`sethook`) | 注释掉缺口段落,保留可跑部分;在文件头标注「裁剪了哪些行 + 为什么(指向 [10](./10-stdlib.md) §14 缺口)」 |
| **缺口(P1 不跑)** | 整体依赖 P1 未实现子系统(`package` 系统、字节码 `string.dump` 往返、`api.lua` 的 C API) | 整文件标 `// SKIP-P1: <原因>`,留待对应子系统实现后启用 |

**官方测试文件 → P1 分类的预判**(实现时以实际依赖为准,这里给策略锚点):

| 官方文件 | 测什么 | P1 分类 | 关键裁剪点 |
|---|---|---|---|
| `literals.lua` | 词法:数字/字符串字面量/转义/长串 | 可直接跑 | 对拍 [03](./03-frontend-lexer.md) 的字面量解析 |
| `constructs.lua` | 语法结构:控制流/作用域/运算符优先级 | 可直接跑 | 对拍 [04](./04-frontend-parser-codegen.md) codegen |
| `calls.lua` | 函数调用/递归/尾调用/多返回值 | 可直接跑 | 尾调用栈不增长([05](./05-interpreter-loop.md) §7.5) |
| `closure.lua` | 闭包/upvalue/作用域捕获 | 可直接跑 | upvalue 关闭流程([05](./05-interpreter-loop.md) §8.3) |
| `vararg.lua` | `...` / `select` | 可直接跑 | — |
| `math.lua` | 算术/数值库/边界 | 可直接跑 | NaN/Inf/`%.14g`/`fmod`(§5、[10](./10-stdlib.md) §8) |
| `nextvar.lua` | `next`/`pairs`/表增删/`#` | **需裁剪** | `pairs` 序口径(§4.1);依赖遍历序的断言改「排序后比」或标豁免 |
| `sort.lua` | `table.sort` | 可直接跑 | 移植 5.1 auxsort([10](./10-stdlib.md) §7.6),稳定性严格 |
| `strings.lua` | 字符串库/`format` | **需裁剪** | `string.dump` 段跳过;`%.14g`/`%q` 逐字节(§5) |
| `pm.lua` | pattern matching(`find`/`gsub`/...) | 可直接跑 | pattern 严格逐字节([10](./10-stdlib.md) §6.6) |
| `events.lua` | 元方法/元表 | 可直接跑 | 不实现 5.2+ 元方法([07](./07-metatables-metamethods.md) §11) |
| `attrib.lua` | `setmetatable`/`getmetatable`/`module` | **需裁剪** | `module`/`package` 段跳过(P1 缺口) |
| `errors.lua` | 错误/`pcall`/措辞 | **需裁剪** | 措辞严格对齐(§5.4);依赖 `debug` 细节的段跳过 |
| `gc.lua` | GC 行为/`collectgarbage`/finalizer | **需裁剪** | `collectgarbage("count")` 数值豁免(§4.5);`__gc` 顺序严格(§4.4) |
| `coroutine.lua`(或 `coro` 段) | 协程 | 可直接跑 | 状态转移/错误措辞([08](./08-coroutines.md)) |
| `db.lua` | `debug` 库 | **缺口/SKIP** | 依赖 `getlocal`/`sethook`/`getupvalue`(P1 缺口) |
| `api.lua` | C API | **缺口/SKIP** | 望舒无 C API,等价能力是嵌入 API([11](./11-embedding-arena-abi.md)),另写 |
| `code.lua` | 字节码内省(假设官方 opcode) | **缺口/SKIP** | 望舒 opcode 自定义([02](./02-bytecode-isa.md) §0 不二进制兼容),不适用 |

> `code.lua`/`api.lua` 的 SKIP 是**结构性**的:望舒**自定义 opcode 编号且无 C API**([02](./02-bytecode-isa.md) §0「不二进制兼容官方 `.luc`」、[11](./11-embedding-arena-abi.md) 用 arena ABI 替代 C API)。它们测的是「官方字节码格式 / C 栈机」,望舒在这两点上**有意不同**,对拍无意义。等价的内省/嵌入正确性由**自写用例 + 嵌入 API 差分**(§3.5、[11](./11-embedding-arena-abi.md))覆盖。

### 2.2 自写针对性用例:补官方覆盖不到的角落

官方套覆盖语义主体,但**望舒特有的实现决策**需要自写用例钉死(官方套不会测这些,因为它们是望舒的实现选择):

| 自写用例组 | 钉死什么 | 来源文档 |
|---|---|---|
| `arith_boundary` | `%.14g` 边界(`0.1`/`1e300`/`-0.0`/`1/0`/`0/0`)、`MOD`=`a-floor(a/b)*b`、整数循环 `2^53` 精度 | [05](./05-interpreter-loop.md) §4.1/§4.6 |
| `table_order_stable` | 「键集确定」用例的 `pairs` 序(严格口径,§4.1) | [06](./06-memory-gc.md) §9.3 |
| `coercion_edge` | `parseLuaNumber` 接受/拒绝的串(`"0x10"`/`" 5 "`/`"0x1p4"`/`"inf"`/`"nan"`) | [07](./07-metatables-metamethods.md) §5.2、[05](./05-interpreter-loop.md) §13 |
| `error_wording` | 各类型错误的精确措辞 + traceback 行号 | [09](./09-errors-pcall.md) §9.3、§9.4 |
| `metamethod_51only` | 5.1 有的元方法生效、5.2+ 的(table `__len`/`__pairs`)**不**生效 | [07](./07-metatables-metamethods.md) §11 |
| `stdlib_existence` | 5.1 全标准库函数名存在、5.2+ 函数名为 nil | [10](./10-stdlib.md) §12.3 |
| `gc_transparency` | 同脚本在「正常 pacing」与「每次分配 GC」下输出 byte-equal | [06](./06-memory-gc.md) §11、§6 本文 |
| `embedding_dropin` | per-item API 与 gopher-lua 形状兼容、同脚本同输出 | [11](./11-embedding-arena-abi.md) §9 |

### 2.3 组织:按特性分组

```
test/conformance/
  official/              移植官方 5.1 套(可直接跑 + 需裁剪,文件头标注分类)
    literals.lua  constructs.lua  calls.lua  closure.lua  vararg.lua
    math.lua  nextvar.lua  sort.lua  strings.lua  pm.lua  events.lua
    attrib.lua  errors.lua  gc.lua  coroutine.lua
    _skipped/            SKIP-P1 的文件(code.lua/api.lua/db.lua),留档不跑
  own/                   自写针对性用例(§2.2),按特性分组
    lexer/  parser/  arith/  table/  metamethod/  coroutine/
    errors/  closure/  numeric_boundary/  stdlib/
  golden/                每个 .lua 对应一份 .expected(官方 lua5.1 现场跑出的输出)
  runner_test.go         Go test 驱动:跑每个 .lua,比对 golden(§2.4)
```

### 2.4 断言方式:脚本输出比对官方 lua5.1

conformance 的判据是**可观察输出比对**(§3 定义「可观察输出」)。两种获取 golden 的方式:

- **(A)golden file(默认,CI 友好)**:用官方 `lua5.1` 解释器**预先**跑每个 `.lua`,把 stdout+stderr(+退出码)存为 `golden/<name>.expected`,入库。CI 里望舒跑同一脚本,比对 golden。**优点**:CI 无需装官方 lua5.1(golden 已入库),可复现、可审计(golden 改动在 PR diff 里可见)。**缺点**:golden 需随用例一起维护;官方版本固定为 5.1.5(锁版本,§2.6)。
- **(B)现场跑官方(本地 / 强校验 CI 阶段)**:若 CI 环境装了 `lua5.1`,可现场跑官方与望舒**双跑比对**,不依赖入库 golden。用于「golden 是否仍与当前官方一致」的周期性校验(防 golden 腐化)。

```go
// test/conformance/runner_test.go —— golden 比对驱动(方式 A)
func TestConformance(t *testing.T) {
    for _, script := range glob("official/*.lua", "own/**/*.lua") {
        if hasSkipMarker(script) { t.Logf("SKIP %s: %s", script, skipReason(script)); continue }
        t.Run(script, func(t *testing.T) {
            got := runWangshu(t, script)              // 望舒跑脚本,捕获 stdout+stderr+exit
            golden := readFile(goldenPath(script))    // 官方 lua5.1 预跑的输出
            got = normalize(got, script)              // 地址脱敏 / 已知豁免替换(§3.4、§4)
            golden = normalize(golden, script)
            if got != golden {                         // 逐字节比对(normalize 后)
                t.Errorf("conformance mismatch\n--- want(official) ---\n%s\n--- got(wangshu) ---\n%s\n--- diff ---\n%s",
                    golden, got, unifiedDiff(golden, got))
            }
        })
    }
}

// runWangshu:用 cmd/wangshu 或直接 internal API 跑脚本,捕获输出。
func runWangshu(t *testing.T, script string) capture {
    src := readFile(script)
    prog, err := wangshu.Compile(src, "@"+script)   // 编译期错误也是可观察输出(§3.2)
    if err != nil { return capture{stderr: formatCompileErr(err)} }
    st := wangshu.NewState(testOpts())
    var out bytes.Buffer
    st.SetStdout(&out)                                // print/io.write 重定向到 buffer(§3.1)
    rerr := prog.Call(st, nil)                        // 运行期错误 → Go error(09)
    return capture{stdout: out.String(), stderr: errString(rerr), exit: exitOf(rerr)}
}
```

**`normalize`(脱敏 / 豁免)是 conformance 与差分共享的关键**:它把「本质不可逐字节一致」的输出片段替换成占位符,详见 §3.4 与 §4 的逐项豁免规则。**严格口径用例(键集确定的 `pairs`、`%.14g`、措辞)不经 normalize 处理对应片段**,直接逐字节比。

### 2.5 单元层的「黄金字节码」对拍(承 04 §1.1)

[04](./04-frontend-parser-codegen.md) §1.1 选 AST 双遍路线的**首要理由就是「差分测试要稳定可预测的寄存器分配」**,并在 §10 给了与 [02](./02-bytecode-isa.md) §8 逐字节一致的端到端示例。这条**寄存器分配同构**承诺在 `internal/frontend/compile` 的单元层用**黄金字节码测试**钉死(不进 conformance,属包内单测,但口径由本文统一):

```go
// internal/frontend/compile/codegen_golden_test.go
func TestCodegenGolden(t *testing.T) {
    src := `local function f(n) local s=0; for i=1,n do s=s+i*i end; return s end`
    proto := compileMain(t, src)
    got := dumpProto(proto)                    // 反汇编成 04 §10.3 那种文本形式
    want := readGolden("f_horner.bc")          // 与 02 §8 / 04 §10.3 逐字节一致的黄金
    assertEqual(t, want, got)                  // 寄存器号/常量号/跳转标签逐项相同
}
```

**为什么寄存器分配同构是差分的前提**(收口 04 §1.1/§10):层间差分(P3+)要求「同一 Proto 在不同 tier 输出 byte-equal」,而 Proto 是 codegen 产物;若望舒 codegen 与官方 luac 的**寄存器分配不同构**,虽然「望舒解释器 vs gopher-lua」差分仍可成立(两者各跑各的 Proto,只比最终输出),但**「望舒 Proto dump vs 官方 luac dump」这一更强的早期对拍**就失效。P1 用黄金字节码把 codegen 锁成与 [02](./02-bytecode-isa.md) §8 / [04](./04-frontend-parser-codegen.md) §10.3 一致,使「字节码层差分」可在单元层提前暴露 codegen 偏差,而不必等到运行期输出才发现。**注意**:与 gopher-lua 的运行期差分**不要求** Proto 逐字节相同(gopher-lua opcode 不同),只要求最终可观察输出相同;Proto 对拍是对**官方 luac**(同一寄存器式 5.1 ISA 家族)的更强约束,且仅用于自检 codegen,不是 CI 硬门禁。

### 2.6 官方 oracle 版本锁定

- **官方 Lua**:锁 **5.1.5**(5.1 系列最后一版,bug 修订完成)。golden 由 5.1.5 生成,文档记录版本号。
- **gopher-lua**:锁一个具体 commit/tag(差分基准,roadmap §7)。gopher-lua 自身可能有与官方 5.1 的**已知差异**(它是独立实现),这些差异进 §4 的「gopher-lua 已知偏差豁免表」,不算望舒的错。
- 版本升级(如未来对拍 5.1.x 新修订)走 PR,golden 重新生成,diff 可审。

---

## 3. 差分 fuzz harness(`test/difftest`)——本文核心,主防线

> roadmap §5 原则 2:**「每个执行层的输出与解释器 byte-equal,持续 fuzz;这是防『投机错误静默错果』
> (JIT 最危险 bug 类别)的主防线」**。[architecture](../architecture.md) §4 不变式 2:**「层间逐字节
> 差分是 CI 必过门禁」**。本节是这两条的工程化。

### 3.1 可观察输出的精确定义(关键收口)

差分的全部意义在于「比对**可观察输出**」。但「输出」不是只有 print——必须精确定义比对面,否则要么漏掉差异(比对面太窄),要么把本质不可比的东西纳入(比对面太宽,假阳性淹没真 bug)。**定稿:可观察输出 = 下列五项的有序合并**:

| # | 可观察项 | 捕获方式 | 比对粒度 | 备注 |
|---|---|---|---|---|
| O1 | **print / io.write 的字节流** | 重定向 stdout 到 buffer | **逐字节、保序** | 主输出面;含分隔符 `\t`、行尾 `\n`(print 语义) |
| O2 | **顶层返回值** | `Program.Call` 的返回值序列,逐个 `tostring` 后拼接 | 逐字节 | 脚本 `return a,b,c` 的可观察化 |
| O3 | **错误信息(值 + 位置 + traceback)** | 捕获 `*LuaError`,取 `value`(tostring)+ 产生位置 + traceback 文本 | **类型/位置严格,措辞见 §5.4** | 含 `chunk:line:` 前缀、`[C]:`、`(...tail calls...)`([09](./09-errors-pcall.md)) |
| O4 | **副作用的相对顺序** | O1/O3 在同一字节流里**按发生顺序**交织 | 顺序严格 | 「先 print 再报错」与「先报错」可区分;副作用序是语义的一部分 |
| O5 | **退出状态** | 正常结束 / 错误结束 / 退出码 | 离散值相等 | `os.exit` 默认不真退([10](./10-stdlib.md) §14),记逻辑退出态 |

**不属于可观察输出**(VM 内部状态,差分**不比**):对象在 arena 的偏移、GC 何时触发 / 回收了哪块 / freelist 复用、IC 命中与否、dispatch 用 switch 还是 closure、栈扩容时机、Proto 在 Go 堆的地址。这些是实现自由度,Lua 程序无法直接观察(除非经 §4 的「泄漏到可观察面」的少数口子——`tostring` 地址、`pairs` 序、`collectgarbage("count")`,逐项在 §4 处理)。

> **副作用顺序(O4)为什么单列**:同一脚本 `print("a"); error("boom")` 的可观察输出是「`a\n` 然后错误」——若某 VM 把 print 缓冲到错误后才 flush,字节流顺序就变了。把 O1/O3 交织进**同一个有序字节流**(而非分开两个 buffer 最后拼)能抓住这类顺序 bug。实现上 stdout 与 stderr 写入**同一个带时序的 capture sink**,或约定「先收集完整 stdout,错误信息追加在后」并保证 flush 时机一致(三个 VM 用同一约定)。

### 3.2 编译期错误也是可观察输出

脚本可能**编译失败**(语法错误、`control structure too long`、`too many local variables`,[04](./04-frontend-parser-codegen.md) §9)。编译错误的**措辞 + 位置**同样是可观察输出(进 O3),同样要差分:望舒 `Compile` 失败的错误文本 vs 官方 `luac`/`loadstring` 的错误文本。这对 fuzz 尤其重要——随机生成器会大量产出**非法或边界脚本**,编译错误路径是高频路径,其措辞一致性([03](./03-frontend-lexer.md) §11 措辞、[04](./04-frontend-parser-codegen.md) §9 措辞)必须被差分覆盖(§5.4 错误措辞口径同样适用于编译错误)。

### 3.3 三方差分架构(P1 阶段)

P1 的差分对象是 **望舒解释器 vs gopher-lua vs 官方 Lua 5.1**(roadmap §1 点名 gopher-lua 是 P1 差分基准,§7 prior art「gopher-lua:P1 的差分基准」)。三方而非两方,因为三方互相校验能定位「谁错了」:

```
              ┌─────────────────────────────────────────┐
   生成器 ──► │  同一脚本 src(P1 语言子集,§3.7)        │
   (§3.7)     └─────────────────────────────────────────┘
                    │              │               │
                    ▼              ▼               ▼
            ┌──────────────┐ ┌──────────────┐ ┌──────────────┐
            │  望舒(crescent│ │  gopher-lua  │ │ 官方 lua5.1  │
            │  解释器,被测) │ │ (Go,基准)   │ │ (C,oracle)   │
            └──────────────┘ └──────────────┘ └──────────────┘
                    │              │               │
                    ▼              ▼               ▼
              capture(O1..O5)  capture        capture
                    │              │               │
                    └──────────────┴───────┬───────┘
                                           ▼
                              normalize(脱敏/豁免,§3.4/§4)
                                           ▼
                              三方逐字节比对(§3.4 判定矩阵)
```

**判定矩阵**(三方比对后的结论):

| 望舒 vs 官方 | 望舒 vs gopher-lua | gopher vs 官方 | 结论 |
|---|---|---|---|
| == | == | == | **通过**(三方一致) |
| ≠ | ≠ | == | **望舒 bug**(官方与 gopher 一致,望舒孤立错——最强信号) |
| == | ≠ | ≠ | **gopher-lua 与官方有已知偏差**;望舒对齐官方 ⇒ 望舒**对**,记 gopher 偏差豁免(§4) |
| ≠ | == | ≠ | 望舒与 gopher 一致但都偏离官方 ⇒ 多半是「望舒抄了 gopher 的偏差」或共同未实现项,**审查** |
| ≠ | ≠ | ≠ | 三方全不同 ⇒ 可能命中「本质未定义」行为(`pairs` 序、地址),查 §4 是否该豁免;否则望舒 bug |

> **官方是最终 oracle,gopher-lua 是「同生态参照 + 性能基准」双重身份**。roadmap §4 验收明确写「**与 gopher-lua 差分 fuzz 输出逐字节一致**」——所以「望舒 vs gopher-lua 逐字节一致」是**硬验收**;但当 gopher-lua 自身偏离官方 5.1 时(它是独立实现,存在已知差异),以**官方为准**,把 gopher 的偏差点登记进豁免表(§4.7),该点改为「望舒 vs 官方」比对。这解决了「验收要求对齐 gopher,但 gopher 可能错」的张力:**默认对齐 gopher,gopher 错的地方对齐官方并豁免 gopher**。

### 3.4 normalize:脱敏与豁免的统一管线

三方输出在比对前都过 `normalize`,把「本质不可逐字节一致的片段」替换为占位符。这是 conformance(§2.4)与差分共享的同一管线:

```go
// test/difftest/normalize.go
func normalize(out string, opts normOpts) string {
    // N1: 对象地址脱敏 —— "table: 0x55f3a2b1c0d0" → "table: 0xADDR"
    out = reAddr.ReplaceAllString(out, "${type}: 0xADDR")   // §4.3
    // N2: math.random 序列 —— 差分用例禁用 random(§4.4),若漏用则脱敏为占位
    // N3: collectgarbage("count") 数值 —— "1024.5" → "<GCKB>"(§4.5)
    // N4: os.date locale 相关字段 —— 月/星期名脱敏(§4.6),仅当用例开启 date 豁免
    // pairs 序不在此处理 —— 由生成器/用例分类决定严格 or 排序比(§4.1),不是文本脱敏
    return out
}
```

**脱敏 vs 排序的区别(重要)**:
- **脱敏(N1-N4)**是「把不可比的**值**抹成占位符」——地址、随机数、GC 字节数、日期名。三方都抹同一占位符后比对结构。
- **排序豁免(§4.1 `pairs`)**不是脱敏——它是「把**一组无序行**排序后再比」,处理的是**顺序**不可比而非值不可比。两者机制不同,§4 分别处理。

### 3.5 嵌入 API 差分(承 11 §9)

除了「脚本喂 VM」,还有「**宿主 Go 代码经嵌入 API 驱动 VM**」这条路径([11](./11-embedding-arena-abi.md) §9 drop-in)。这条也要差分:同一段「Go 胶水 + Lua 脚本」分别用 gopher-lua API 和望舒 per-item API 跑,比对输出。这覆盖「`State.Register` 的 host function、`Push*/To*` 栈机、`CallGlobal`」等 API 行为的 5.1 兼容性,是 drop-in 承诺([11](./11-embedding-arena-abi.md) §9.2)的差分兜底。P1 用一组**手写的嵌入差分用例**(Go test),不做 API 调用序列的 fuzz(留 P2+)。

### 3.6 失败用例最小化(fuzz → conformance 固化)

fuzz 撞出差异后,随机脚本通常很大、含无关噪声。harness 自带**最小化(delta-debugging / shrinking)**:对失败脚本反复「删一段/简化一个字面量 → 是否仍触发差异」,收敛到最小复现。最小化后的脚本:① 写进失败报告(便于人读);② **固化为一条确定性 conformance 用例**(§1 末尾「fuzz 撒网 → conformance 固化」闭环)入 `own/`,golden = 官方输出。这样同一 bug 的回归不再依赖随机种子撞中。

```go
// test/difftest/shrink.go
func shrink(src string, stillDiffers func(string) bool) string {
    cur := src
    for changed := true; changed; {
        changed = false
        for _, cand := range reductions(cur) {   // 删语句/删表达式/常量替 0/删分支...
            if isValidP1(cand) && stillDiffers(cand) { cur = cand; changed = true; break }
        }
    }
    return cur                                     // 最小复现
}
```

### 3.7 fuzz 输入生成

约束在 **P1 支持的 Lua 5.1 语言子集**(全语言核心 + 已实现 stdlib;避开 P1 缺口:`module`/`string.dump`/高级 `debug`/`os.execute`)。两种生成器,P1 都建:

**(a)语法制导生成(grammar-based,主力)**——按 Lua 5.1 文法 + 语义约束随机展开,**保证生成的脚本语法合法且尽量语义合法**(高「有效脚本率」,大部分 fuzz 时间花在真正执行而非编译报错):

```go
// test/difftest/gen/grammar.go —— 语法制导生成器(概要)
type Gen struct {
    rng    *rand.Rand
    scope  *scopeStack       // 已声明的局部/全局/函数名,保证引用已定义变量(语义合法)
    depth  int               // 嵌套深度(限制,防爆栈 / 防超大脚本)
    vararg bool              // 当前函数是否 vararg(决定 '...' 是否合法,04 §7.3)
    inLoop bool              // 是否在循环内(决定 break 是否合法,04 §6.7)
}

func (g *Gen) genBlock() string {
    var b strings.Builder
    for n := g.rng.Intn(maxStmts); n > 0; n-- { b.WriteString(g.genStmt()) }
    if g.rng.Float64() < pReturn { b.WriteString(g.genReturn()) }   // 末位 return
    return b.String()
}

func (g *Gen) genStmt() string {
    switch g.weightedPick(stmtWeights) {     // 加权:常见结构权重高
    case kLocal:   name := g.fresh(); g.scope.declare(name); return "local "+name+" = "+g.genExpr(0)+"\n"
    case kAssign:  return g.genLValue()+" = "+g.genExpr(0)+"\n"
    case kIf:      return "if "+g.genExpr(0)+" then\n"+g.nested(g.genBlock)+"end\n"
    case kNumFor:  return g.genNumFor()       // for v=a,b[,c] do ... end(inLoop=true 进体)
    case kCall:    return g.genCallStmt()      // 调用已声明的函数/全局
    case kBreak:   if g.inLoop { return "break\n" }; return ""   // 仅循环内
    // ... while/repeat/do/localfunc/genfor
    }
}

func (g *Gen) genExpr(prec int) string {
    // 字面量 / 已声明变量引用 / 二元(按优先级,04 §4.3)/ 一元 / 调用 / table 构造 / 函数字面量
    // 数字字面量偏向边界值(0, -0.0, 大数, 小数, 整数)以撞 %.14g / 溢出口径
}
```

设计要点:
- **引用已声明变量**(`scope` 跟踪):避免生成一堆 `nil` 全局访问(虽合法但低信息量);偶尔故意引用未声明名以测全局/nil 路径。
- **加权分布**:常见结构(算术、调用、if、for)高权重,罕见结构(深嵌套闭包、长 concat 链)低权重但非零——既覆盖主体又撒到角落。
- **语义约束内建**:`break` 仅 `inLoop`、`...` 仅 `vararg`、深度上限——让生成的脚本**大概率执行成功**(而非编译失败),把 fuzz 算力投到执行差异上。
- **偏向「分配密集」**(table 构造、字符串 concat、闭包)以同时喂 GC 压力 fuzz(§6)。

**(b)种子变异(mutation-based,补充)**——以 conformance 套和真实脚本(首个宿主的规则脚本)为**种子**,做小变异(改常量、换运算符、删/复制语句、调换顺序)。变异保留大部分结构,容易生成「接近真实但有微妙差异」的脚本,擅长撞「真实负载边界」的 bug。变异后**校验语法合法**(过 parser),非法则丢弃或回退。

> P1 优先 (a)(覆盖语言子集系统、有效率高),(b) 作为「真实负载导向」补充。两者共用 §3.3 的三方比对与 §3.6 最小化。生成器**自身的语言子集约束**随 stdlib 实现推进而放宽(P1 早期只生成核心语言,stdlib 就绪后加入库调用)。

### 3.8 未来层间差分(P3+):harness 先建好,加 tier 即接入

[architecture](../architecture.md) §4 不变式 2 的最终形态是「**同一 Proto 在 crescent/gibbous/fullmoon 上执行,输出 byte-equal**」。P1 阶段没有 gibbous/fullmoon,但 harness 的**接口先抽象成「VM runner」**,P1 接三个 runner(望舒解释器 / gopher-lua / 官方),P3+ 只**新增 runner**(gibbous-wasm / fullmoon-trace)接入同一比对框架:

```go
// test/difftest/runner.go —— VM runner 抽象(P1 建好,P3+ 加实现)
type Runner interface {
    Name() string
    Run(src string, arena []byte) (capture, error)   // 跑脚本,返回 O1..O5
}

// P1 的三个 runner:
type WangshuInterp struct{}   // 被测:internal/crescent
type GopherLua struct{}        // 基准:github.com/yuin/gopher-lua
type OfficialLua struct{}      // oracle:exec lua5.1 子进程(或 golden 文件)

// P3 已实装(承 ../p3-wasm-tier/08-testing-strategy.md):
// type WangshuGibbous struct{} // 同一 Proto 走 Wasm 层(P3 build wangshu_p3+wangshu_profile,
//                              // force-all 升 gibbous → wasm 翻译产物执行)
// P4 已实装(承 ../p4-method-jit/08-testing-strategy.md §4 / §5,RJ-1/RJ-2):
// type WangshuGibbousJIT struct{} // 同一 Proto 走 mmap+RX 原生码(P4 build wangshu_p4+wangshu_profile,
//                                 // force-all 升 gibbous → 字节级 inline 模板 +
//                                 // host helper round-trip 混合执行)
// P5 预留:
// type WangshuFullmoon struct{} // 同一 Proto 走 trace JIT(p5-trace-jit)

func DiffN(src string, runners ...Runner) DiffResult { /* N 方比对,§3.3 矩阵推广 */ }
```

**P3+ 接入的关键差异**:P1 的三方是「**不同实现各跑各的字节码**,只比最终输出」;P3+ 的「望舒解释器 vs 望舒 gibbous」是「**同一份 Proto** 走不同执行层」——后者是更强的差分(同输入字节码,任何输出差异必是执行层 bug,不存在「实现本来就不同」的噪声)。这正是 roadmap §5 把它当**JIT 投机错误主防线**的原因:trace JIT 的去优化(deopt)若漏了某个 guard,投机路径会**静默产出错误结果**,只有「同 Proto 走解释器 vs 走 JIT 输出对比」能逐字节抓住(§7 前瞻详述)。**P4 已兑现该主防线**(承 [P4 08 §4 V1-V13 + §5 V17-V22](../p4-method-jit/08-testing-strategy.md)):58 difftest 三方 byte-equal(oracle / crescent / p4-jit force-all)+ 26 e2e prove-the-path + V22 fuzz harness。P1 把这套框架建好,是给 P3+ 的「主防线」提前铺好轨道(roadmap §3 原则:每阶段独立交付,P1 的差分框架本身就是交付物)。

---

## 4. 不确定性源的处理(逐条收口各文档指向 12 的口径)

这是 §10 总表的展开论证。每一项都明确:**哪个文档哪节提出 → 12 的最终决策 → 理由 + 机制**。

### 4.1 pairs / next 遍历序(最重要的收口,承 06 §9.3/§11、01 §8、02 §10、07 §11、10 §13.1)

**问题来源**:`pairs`/`next` 的遍历顺序由表的 node 段布局决定,而布局取决于①字符串键的哈希值②`hmask`(node 段大小)③rehash 算法④Brent 冲突链让位⑤数组段与哈希段拼接顺序。Lua 语义**允许** `pairs` 序未定义。多篇文档把「`pairs` 序是否要求逐字节一致」明确标为「验收口径问题,由 12 定」:[01](./01-value-object-model.md) §8、[02](./02-bytecode-isa.md) §10、[06](./06-memory-gc.md) §9.3/§11/§13、[07](./07-metatables-metamethods.md) §11、[10](./10-stdlib.md) §13.1。

**12 定稿:混合口径——按「键集是否确定」分流,默认严格、本质未定义则排序豁免。**

| 用例类别 | 口径 | 判据 | 机制 |
|---|---|---|---|
| **键集确定的 `pairs`** | **严格逐字节** | 脚本里表的键集合在 `pairs` 前**完全确定**(无依赖 rehash 时机的增删序) | 不脱敏不排序,逐字节比三方遍历输出 |
| **遍历序本质未定义** | **排序后比较** | 表经历复杂增删 / 键集依赖运行期 / 非序列表的 `#` 边界 | 把 `pairs` 产出的「键-值行」收集、排序后再比三方 |

**为什么不统一用「排序豁免」(更省事)**:排序豁免**削弱差分强度**——它放弃了「遍历序」这个可观察维度,而遍历序恰恰是「JSHash + rehash + Brent 是否真与官方一致」的**唯一外部探针**([06](./06-memory-gc.md) §9.3 把哈希环锁死与官方一致,正是为了让这个探针可用)。若一律排序,那么望舒即便哈希算法写错(只要键值集合对)也能差分通过——这放走了一整类 bug。**对「键集确定」用例用严格口径,把这个探针用满**;只对「本质未定义」用例豁免,把假阳性挡掉。

**为什么严格口径在「键集确定」时可行**(承 06 §9.3 的论证):[06](./06-memory-gc.md) §9.3 已定稿用 **Lua 5.1 JSHash 分段采样**(否决 FNV-1a),把字符串键哈希锁成与官方**逐位一致**;[01](./01-value-object-model.md) §5.2 / [06](./06-memory-gc.md) §11 声明 rehash(`luaH_resize`)、Brent 变体、数组/哈希遍历拼接都**对照 `ltable.c` 对齐**。当这五环全部对齐且键集确定(无 rehash 时机依赖)时,望舒、官方、gopher-lua 的 node 布局**应当一致**,遍历序逐字节可比。**gopher-lua 的 `pairs` 序若与官方不同**(它的表实现可能偏离),则该用例落入 §3.3 矩阵的「gopher 偏差」行,以官方为准,gopher 在该点豁免(§4.7)。

**「键集确定」的自动判定**(生成器侧):语法制导生成器(§3.7)在生成涉及 `pairs` 的脚本时,**标注**该 `pairs` 的目标表是否「键集确定」(生成器知道自己有没有插入依赖时机的增删)。确定 → 严格;不确定 → 生成时就给该 `pairs` 输出包一层 `sort`(脚本层排序)或在 normalize 标记排序。conformance 自写用例(`table_order_stable`)显式构造「键集确定」场景走严格口径。

> **收口结论一句话**:`pairs` 序**默认严格逐字节**(用满哈希一致性探针),**仅对「遍历序本质未定义」的用例排序豁免**。这把 [06](./06-memory-gc.md) §9.3 锁死的哈希环价值最大化,同时挡住假阳性。`cleartable`(弱表清死键,[07](./07-metatables-metamethods.md) §1165)影响 `pairs` 序的项归入「本质未定义」(键的存活依赖 GC 时机)→ 排序豁免。

### 4.2 数字 → 字符串格式(承 05 §4.6/§13、03 §11.x、07 §730、10 §13.1)

**问题来源**:`tostring(number)`、CONCAT、`table.concat`、`string.format` 的 `%d/%f/%g/%e/%.14g` 把数字转字符串。[05](./05-interpreter-loop.md) §4.6 把 CONCAT 的 `%.14g` 标「与差分基准逐字节一致,留 12 验收」;[03](./03-frontend-lexer.md) 把字面量折叠口径标「与运行期逐字节同结果」;[10](./10-stdlib.md) §13.1 把 format 浮点列为「严格逐字节(复刻 C printf)」。

**12 定稿:必须逐字节一致,核对方法 = 对官方 lua5.1 输出。**

- **`tostring`/CONCAT 的数字格式 = Lua 5.1 `LUAI_NUMFMT` = `"%.14g"`**。望舒用 Go 实现 `%.14g` 时**必须复刻 C `printf("%.14g")` 的精确行为**(尾零处理、指数位数 `e+NN` 两位、负零 `-0`、`inf`/`nan` 拼写)。Go 的 `strconv.FormatFloat(f, 'g', 14, 64)` 与 C `%.14g` **有微差**(指数位数、`+` 号、`inf`/`nan` 文本),不能直接用——需校准(见下)。
- **`string.format` 的 `%f/%g/%e/%d/%x/...`**:同样复刻 C printf 语义([10](./10-stdlib.md) §5.2.1)。
- **校对方法**:`benchmarks`/conformance 里建一组**数字格式笛卡尔积用例**(各种代表浮点 × 各种 spec),golden = 官方 lua5.1 输出,望舒逐字节对拍。代表浮点必含:`0.0`、`-0.0`、`0.1`、`1/3`、`1e300`、`1e-300`、`2^53`、`2^53+1`、`math.huge`、`-math.huge`、`0/0`(NaN)、整数值 `42.0`(应输出 `42` 不是 `42.0`,这是 `%.14g` 的整数化行为)。
- **NaN/Inf 文本**:Lua 5.1 在多数平台输出 `inf`/`-inf`/`nan`(或 `-nan`,平台相关)。这是**平台相关项**——官方 5.1 的 NaN/Inf 文本依赖底层 libc。望舒固定输出与**锁定的官方 5.1.5 参照平台**一致(§2.6 锁版本同时锁参照平台的 libc 行为);若 gopher-lua 输出不同(它用 Go 的 inf/nan 文本),落 §3.3 gopher 偏差行,以官方为准。

> **关键纪律**:折叠口径与运行期一致由「同一 `value.NumberValue` + 同一格式化函数」保证([03](./03-frontend-lexer.md) §11、[04](./04-frontend-parser-codegen.md) §13 已承诺 codegen 折叠走与解释器同一 `NumberValue`)。所以「编译期 `1/0` 折叠 vs 运行期 `1/0`」天然同结果——format 函数只此一份,折叠与运行期都调它。

### 4.3 table 地址 / tostring(table) 等(承 07 §11/§982、10 §13.1)

**问题来源**:`tostring({})` / `print({})` 输出 `table: 0x...`(含对象地址)。[07](./07-metatables-metamethods.md) §11 / [10](./10-stdlib.md) §13.1 明确:arena 偏移 ≠ C 指针 ≠ gopher-lua 的 Go 指针,**必然不同,不可能逐字节一致**,12 必须豁免。同理 `function: 0x...`、`thread: 0x...`、`userdata: 0x...`(无 `__tostring` 时的默认格式)。

**12 定稿:正则脱敏地址为占位符 `0xADDR`(normalize N1),三方都脱敏后比对结构。**

```
正则:^(table|function|thread|userdata): 0x[0-9a-fA-F]+$   →   ${1}: 0xADDR
```

- 脱敏**只抹地址数字,保留类型前缀**——这样「`tostring(t)` 返回的是 table 而非 function」这一**类型信息仍被差分**(类型错了仍能抓),只放过不可比的地址值。
- **有 `__tostring` 元方法**时,`tostring` 走元方法返回自定义串([07](./07-metatables-metamethods.md)),那是确定性输出,**不脱敏、严格比**——�crubbing 只针对「默认地址格式」。
- 生成器对「裸 print table」可选择性减少(降低脱敏依赖),但不禁止(豁免管线已处理)。

### 4.4 math.random(承 10 §13.1/§8.4)

**问题来源**:`math.random` 的具体序列依赖底层 PRNG。Go 的 `math/rand` ≠ C 的 `rand()`/`random()`,**确定 seed 下序列也不同**([10](./10-stdlib.md) §8.4),不可能逐字节一致。

**12 定稿:差分用例默认禁用 `math.random`/`randomseed`(生成器不产出),需要时只验范围。**

- **生成器禁产 random**:语法制导生成器(§3.7)的库调用白名单**排除** `math.random`/`math.randomseed`——从源头避免随机序列进入差分输出。
- **conformance 里的 random 用例**:只断言**范围正确性**(`math.random()` ∈ [0,1)、`math.random(1,6)` ∈ {1..6} 且为整数),不比具体值(自写用例,非 golden 对拍)。
- 若某变异种子(§3.7b)意外引入 random,normalize 把 random 产出脱敏(N2),或该用例标豁免。

### 4.5 collectgarbage("count") / gcinfo / GC 数值(承 10 §13.1)

**问题来源**:`collectgarbage("count")` 返回内存 KB 数,望舒的 arena 内存模型 ≠ C 堆 ≠ gopher-lua 的 Go 堆,数值必然不同([10](./10-stdlib.md) §4.6/§13.1)。

**12 定稿:数值脱敏(normalize N3),只验「调用不报错、返回数字类型」,不比具体值。** 生成器对 `collectgarbage("count")`/`gcinfo` 的产出做脱敏占位。

### 4.6 os.date / locale(承 03 locale、10 §13.1)

**问题来源**:`os.date` 的 `%c/%x/%X`、月/星期名、时区依赖 C locale;望舒纯 Go 无 `setlocale`([10](./10-stdlib.md) §9.2/§9.4)。[03](./03-frontend-lexer.md) 也把标识符 locale 标 ASCII-only 以保差分可复现。

**12 定稿:**
- **os.date locale 相关字段**(月名/星期名/`%c`):**部分豁免**——脱敏这些字段(N4),只严格比对**数值型字段**(`%Y/%m/%d/%H/%M/%S`,纯数字、locale 无关)。生成器对 `os.date` 偏向数值 spec。
- **lexer 标识符 ASCII-only**([03](./03-frontend-lexer.md)):这不是豁免而是**口径锁定**——望舒标识符严格 `[A-Za-z0-9_]`,差分用例(生成器)只产 ASCII 标识符。若 gopher-lua 在高位字节标识符上与望舒不同,登记 gopher 偏差豁免(§4.7),以「ASCII-only 是望舒明确口径」为准。

### 4.7 错误信息:措辞 / 位置 / traceback(承 09 §9.3/§314/§673、03 措辞、07 §1127、08 §189、10 §13.1)

**问题来源**:错误信息措辞被**大量**文档标「待 12 差分核对」:[09](./09-errors-pcall.md) §9.3(`bad argument` 措辞)/§314(traceback 行号)/§673(深调用链行号)/§862(单复数);[03](./03-frontend-lexer.md) §11(词法错误措辞);[04](./04-frontend-parser-codegen.md) §9(编译错误措辞);[07](./07-metatables-metamethods.md) §1127(类型错误冠词/标点);[08](./08-coroutines.md) §189(协程错误措辞);[10](./10-stdlib.md) §13.1(argError 措辞)。这些文档共同的纪律是「**给骨架,不编造精确标点,以官方 Lua 5.1 为 oracle 钉死**」。

**12 定稿:三级口径——类型/位置必须一致,精确措辞 P1 目标逐字节对齐官方,但允许已知豁免清单。**

| 层级 | 口径 | 理由 |
|---|---|---|
| **错误类型**(算术错 / 索引错 / 调用错 / 语法错 / stack overflow) | **严格一致** | 类型是语义,差分必须抓「该报错却不报 / 报错类型不对」 |
| **错误位置**(`chunk:line:` 前缀、`[C]:`、traceback 每行行号) | **严格逐字节** | 位置是 5.1 明确行为,行号错是真 bug([09](./09-errors-pcall.md) §314 用多行脚本钉死);traceback 的 `(...tail calls...)`/`[C]: in function 'x'` 结构严格 |
| **精确措辞**(冠词 `a`/`an`、单复数、标点、`got no value` 等) | **P1 目标逐字节对齐官方**;**已知不可对齐项进豁免清单** | 措辞 95% 可对齐(移植 5.1 消息模板);少数平台/版本相关措辞(libc errno 文本、`os` 错误)进豁免 |

**措辞对齐的实现路径**:望舒的错误消息模板**逐字移植 Lua 5.1 源码**的格式串(`lvm.c`/`ldebug.c`/`lauxlib.c` 的 `luaG_*`/`luaL_*` 消息)。差分套件用**错误措辞笛卡尔积用例**(每种错误 × 各种触发上下文)对拍官方,撞出的措辞差异**逐条修正模板**直到对齐。**无法对齐的(豁免清单,§4.8 维护)**:
- libc 相关错误文本(`io.open` 失败的 errno 描述,纯 Go 与 C 不同)→ 脱敏错误细节,只比错误类型 + 「io 操作失败」结构。
- gopher-lua 自身措辞偏差(它的消息可能与官方 5.1 不完全一致)→ 以官方为准,gopher 在该措辞点豁免。

> **为什么措辞要这么较真**(roadmap §5 原则 2 在错误层的体现):错误措辞看似无关紧要,但**很多真实脚本用 `pcall` + 匹配错误消息做控制流**(`if err:match("attempt to index") then ...`)。措辞偏差会让这类脚本在望舒上行为分叉。且措辞对齐是「望舒是否真的逐字移植了 5.1 语义」的强信号——措辞对了通常意味着错误路径的逻辑也对了。所以 P1 把措辞当**逐字节目标**(非「差不多就行」),只对**本质不可控**项豁免。

### 4.8 豁免清单的维护(防豁免滥用)

豁免是「合法的不比对」,但豁免**滥用会掏空差分强度**(什么都豁免就什么都测不出)。纪律:

- **豁免清单集中在一处文件** `test/difftest/exemptions.go`,每条豁免**必须注明**:① 豁免什么(正则/用例标记)② 根因(为什么本质不可比,指向本文 §4.x)③ 范围(全局脱敏 / 仅某用例)。
- **豁免分两类**:**结构性豁免**(地址、random、GC 数值、locale、libc 文本——本质不可比,§4.3-§4.6)是永久的;**gopher 偏差豁免**(§4.7)是「gopher-lua 偏离官方,望舒对齐官方」,**记录但以官方为准**,理论上可随 gopher 修复而移除。
- **新增豁免需 review**:CI 里豁免清单的改动**高亮**(像 golden 改动一样在 PR diff 显眼),防「为让 fuzz 过而偷偷加豁免掩盖真 bug」。
- **严格口径项绝不豁免**:`pairs` 严格用例、`%.14g`、措辞(非豁免项)、寄存器分配同构——这些是差分强度的核心,任何「想豁免它们」的提案都要回到本文重新论证。

### 4.9 嵌入式 hardening 阈值(对位差异的特殊类:宿主不可崩优先)

**优先级**:**宿主进程不可崩 > 与官方/gopher-lua 字节一致**(roadmap §0 的「不可妥协」)。当对位 backend(PUC 5.1.5 / gopher-lua)自身不防御某类输入会导致**Go runtime fatal**(`out of memory` / stack overflow),而我们必须防,则:

- **该处主动 fail-fast**(返 Lua 错误,可被 pcall 兜住)——与对位 backend 不同行为(对位是 OOM crash 进程,我们返错误);
- 该差异**不算豁免也不算 bug**,而是「嵌入式 hardening 阈值」类**主动偏离**;
- 阈值选择口径:取「实际工程使用的上限 × 一个数量级」并圆整到 2^N。
  - **`string.rep`**:`len(s) * n > 1<<30`(1 GiB)→ `"string length overflow"`
  - **`string.format`**:width / precision > `1<<30` → `"invalid format width/precision"`
  - **`table.concat`**:`j - i > 1<<24`(~16M)→ `"table.concat range too large"`
- 后续新增 hardening 阈值时统一沿用 1 GiB 量级(分配类)/ 1<<24 量级(循环类),除非有具体业务场景需求理由。

**为什么不直接对位 PUC**:PUC Lua 是非嵌入式场景设计,`string.rep("k", 1e14)` 直接 OOM 是「设计行为」(脚本错让宿主死)。我们的承诺更高(嵌入式 VM,宿主进程一定不可崩),hardening 阈值是这个承诺的兑现。**首次踩坑**:fuzz corpus `testdata/fuzz/FuzzCompileRun/2abea9243c4e4b41`(`string.rep("k", 1e14)`)在 v0.1.3 区间外部审计阶段触发 Go runtime OOM fatal,fix 见对应 commit。

**纪律**:hardening 阈值的引入是「破对位行为」,必须满足:① 不可恢复的 runtime 崩溃(OOM / stack overflow 等 Go 无法 recover 的)② fail-fast 返 Lua 错误③ commit message 与 godoc 写明背景。仅性能差异 / 内存效率差异**不**触发 hardening。

---

## 5. GC 压力 fuzz(收口 06 §11/§6.3/§5.2、10 §13.3)

> [06](./06-memory-gc.md) §11 把 GC 压力 fuzz 列为「§6.3 shadow stack 纪律、§5.2 mark 完整性的**主要自动化防线**」,
> 并明确「对 12 的接口要求(本文提出,12 定稿)」。[10](./10-stdlib.md) §13.3 把它列为「捕获 stdlib shadow
> stack 漏 Pin 的主手段」。本节定稿机制。

### 5.1 两个验证目标

GC 压力 fuzz = **把 GCPAUSE 设到极小(每次 / 每几次分配就 full GC),反复跑同一程序**,验证两件事:

| 目标 | 验证什么 | 抓什么 bug |
|---|---|---|
| **① GC 透明性** | 同一脚本在「正常 pacing」与「极高频 GC」下,**可观察输出 byte-equal** | GC 改变了可观察行为(违反 [06](./06-memory-gc.md) §11「GC 不应改变可观察行为」)——通常是 mark 漏扫某字段([06](./06-memory-gc.md) §5.2)导致对象内容被错误回收/复用 |
| **② 不崩溃** | 极高频 GC 下脚本跑完不 panic / 不脏读 | 漏 push shadow stack([06](./06-memory-gc.md) §6.3)——host 持有的中间对象在分配窗口被回收;mark 漏扫([06](./06-memory-gc.md) §5.2)——某类对象的某 GCRef 字段没被遍历,被误回收 |

### 5.2 为什么高频 GC 是这类 bug 的「必现」手段

[06](./06-memory-gc.md) §6.3 点破:漏 push shadow stack / mark 漏扫**在正常 pacing 下偶发**——GC 恰好在「对象被持有但未上根」的窗口触发的概率很低,bug 偶现、极难复现、是「最难调的 bug 类」。**高频 GC 把概率拉到 1**:若每次分配都 full GC,那么「分配 B 时 A 还在 Go 局部未上根」的窗口**必然撞上一次 GC**——漏 push 的 A 必被回收,后续用 A 必崩(或脏读)。同理 mark 漏扫的字段,每轮 GC 都重新标记,漏扫的对象**每次**都成死白被回收。**把偶发 bug 变成确定 bug**,这是 GC 压力 fuzz 的全部价值。

### 5.3 机制

```go
// test/difftest/gcfuzz.go
func TestGCStress(t *testing.T) {
    // 用分配密集的脚本(生成器 §3.7 偏向 table/concat/closure,或 conformance 分配密集用例)
    for _, script := range allocHeavyScripts(t) {
        // 基线:正常 pacing 跑一次,取输出
        baseOut := runWithGCPause(t, script, /*GCPAUSE=*/200)   // 默认 2.0x(06 §8.3)
        // 压力:极小 GCPAUSE 反复跑
        for _, pause := range []int{1 /*每分配即 GC*/, 2, 5} {
            stressOut, err := runWithGCPauseSafe(t, script, pause)
            if err != nil {
                t.Errorf("GC stress CRASH @pause=%d %s: %v", pause, script, err)  // 目标②:崩溃
            }
            if stressOut != baseOut {
                t.Errorf("GC NOT transparent @pause=%d %s\nbase:%s\nstress:%s",   // 目标①:透明性
                    pause, script, baseOut, stressOut)
            }
        }
    }
}

// runWithGCPause:用测试 opts 把 collector 的 GCPAUSE 调到指定值(06 §8.3 的 threshold = live*GCPAUSE/100)。
// pause=1 ⇒ threshold≈live*1% ⇒ 几乎每次分配都越阈值触发 full GC。
// runWithGCPauseSafe:额外用 recover 兜底,把 panic 转成 err(否则崩溃测试无法继续)。
```

要点:
- **GCPAUSE 调小是测试钩子**:[06](./06-memory-gc.md) §8.3 的 `threshold = live * GCPAUSE / 100`,把 GCPAUSE 设 1 即「存活量 1% 增量就 GC」≈ 每次分配 GC。这需要 collector 暴露**测试可调的 GCPAUSE**(`testOpts` 注入)。
- **基线对照是关键**:不是「高频 GC 跑通就行」,而是「高频 GC 输出 == 正常 pacing 输出」。透明性(目标①)比不崩溃(目标②)更强——不崩溃只证明没 use-after-free,透明性还证明没「悄悄回收了不该回收的、导致输出错」。
- **与三方差分叠加**:GC 压力 fuzz 主要是**望舒内部**的「高频 vs 正常 GC」对照(gopher-lua/官方的 GC 不可比)。但若高频 GC 下望舒输出变了,它同时也会偏离官方 → 三方差分也会抓到。GC 压力 fuzz 是「更早、更针对」的捕获。
- **stdlib 分配类重点**([10](./10-stdlib.md) §13.3):`string.format`/`concat`/`rep`/`gsub`/`match` 这些 host function 内分配中间对象的([06](./06-memory-gc.md) §6.3 纪律对象),是 GC 压力 fuzz 的主要轰炸目标——它们的 shadow stack 漏 Pin 在高频 GC 下必现。

### 5.4 finalizer 顺序(承 06 §10/§11)

`__gc` 终结器的调用顺序([06](./06-memory-gc.md) §10:**创建逆序**)若被测试观察(终结器里 print),须与 Lua 5.1 一致。这是确定性差分用例(`own/gc/finalizer_order.lua`):构造多个带 `__gc` 的 userdata,让它们终结时 print 标识,断言输出顺序 = 创建逆序,对拍官方。**注意**:[06](./06-memory-gc.md) §12 缺口提到 P1 可不支持「多次终结(复活后再登记)」——依赖多次终结的用例标 P1 已知限制豁免。

---

## 6. 基准测试(`benchmarks/baseline`,收口 05 §3/§2.2、roadmap §1/§4)

> roadmap §4 P1 验收:**「简单 / 算术 / 循环三档脚本全部 ≥2x over gopher-lua」**。
> [05](./05-interpreter-loop.md) §3 给了 ≥2x 的可达性论证与三档定义;§2.2 给了 dispatch spike 的 A/B 口径。
> 前提一([design-premises](../../../llmdoc/must/design-premises.md) 前提一 / roadmap §1):**基准必须用「列内核」形状,否则边界成本主导,
> 测不出 VM 加速**。本节定稿三档脚本、对照组、测量方法、A/B 口径,并把 roadmap 校准数据入库。

### 6.1 列内核形状是基准的硬约束(收口前提一)

roadmap §1 / 前提一钉死:项目收益**只在列内核形状下兑现**——**循环写在 Lua 内,一次调用进一次 VM,整批数据在 VM 内迭代**。基准**必须**用这个形状,否则:若按 per-item(Go for 循环里反复调一个单行 Lua 函数),**边界跨越 + 值装箱的几十~百 ns 固定成本主导**(前提二),VM 本体加速被稀释到测不出(roadmap §1 校准测量 2:端到端落 ±5-7% 噪声带)。

**落到 benchmark 代码**:被测的「一次调用」里,Lua 侧是一个**完整循环**(整批迭代),而非单次运算:

```go
// benchmarks/baseline/horner_test.go —— 列内核形状(正确)
func BenchmarkHornerWangshu(b *testing.B) {
    src := `
        local function horner(a0,a1,a2,a3,a4,a5, xs, n)
          local sum = 0
          for i=1,n do                       -- ★ 循环在 Lua 内,整批迭代
            local x = xs[i]
            sum = sum + (((((a5*x+a4)*x+a3)*x+a2)*x+a1)*x+a0)
          end
          return sum
        end
        return horner`
    prog, _ := wangshu.Compile(src, "@horner")
    st := wangshu.NewState(benchOpts())
    horner := callReturn(st, prog)           // 取出函数
    xs := makeArenaFloatColumn(1000)         // 1000 items,arena 列(11 §3 ABI)
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _ = st.CallFn(horner, coeffs..., xs, 1000)   // ★ 一次调用,VM 内跑完 1000 次迭代
    }
}

// 反例(不要这样测)——per-item,边界成本主导,测不出 VM 加速:
//   for i := 0; i < b.N; i++ { for j:=0;j<1000;j++ { st.CallFn(单次运算, xs[j]) } }
//   ↑ b.N*1000 次跨界,每次几十~百 ns 边界税,VM 本体加速被淹没(前提一/二)
```

> **这条约束直接来自校准测量**(roadmap §1):真 LuaJIT 只比 luajc 快 6%(154 vs 164μs)——因为 per-item 下边界主导。望舒在列内核形状下才有 ≥2x 空间。**基准不用列内核形状 = 测错了东西**,会得出「望舒没加速」的假结论(实际是被边界税淹没)。所以基准代码 review 的第一条:**确认「一次 CallFn 进 VM 后整批迭代」,而非 Go 侧循环反复跨界**。

### 6.2 三档脚本(收口 05 §3 的三档定义)

[05](./05-interpreter-loop.md) §3.4 定义三档,各档吃不同的加速来源。每档给代表脚本:

| 档 | 主要 opcode | 吃的加速来源(05 §3.4) | 代表脚本 |
|---|---|---|---|
| **简单** | MOVE / LOADK / 比较 / 跳转 | 去装箱 + 跳转表 dispatch | 紧循环里做赋值/比较/分支(无算术无表),如「计数 + 条件累加」 |
| **算术** | ADD/SUB/MUL + FORLOOP | f64 直算零分配 + 算术 IC | **Horner 5 次多项式**(roadmap §1 同款,§6.1 示例) |
| **循环** | FORLOOP 密集 + 表/全局 IC | FORLOOP 回边零开销 + 循环内 IC 复用 | 嵌套循环 + 表读写(`for i do for j do t[k]=t[k]+1 end end`),吃全局/表 IC |

**代表脚本设计原则**:每档**突出该档的加速来源、压制其它噪声**——简单档不放算术(否则测的是算术档),算术档用纯数值列(Horner 是经典,roadmap §1 校准就用它),循环档放表访问(吃 IC,这是列内核典型)。三档都用列内核形状(§6.1)。

```lua
-- benchmarks/baseline/scripts/simple.lua —— 简单档(MOVE/LOADK/比较/跳转)
local function simple(n)
  local c = 0
  for i=1,n do
    if i % 2 == 0 then c = c + 1 else c = c - 1 end   -- 比较+分支+赋值,无重算术
  end
  return c
end

-- benchmarks/baseline/scripts/arith.lua —— 算术档(Horner,roadmap §1 同款)
local function arith(xs, n, a0,a1,a2,a3,a4,a5)
  local sum = 0
  for i=1,n do
    local x = xs[i]
    sum = sum + (((((a5*x+a4)*x+a3)*x+a2)*x+a1)*x+a0)   -- f64 直算密集
  end
  return sum
end

-- benchmarks/baseline/scripts/loop.lua —— 循环档(FORLOOP + 表/全局 IC)
local function loop(t, n)
  local s = 0
  for i=1,n do
    for j=1,10 do s = s + t[j] end        -- 内层表读,t[j] 命中表 IC(同表同形状)
  end
  return s
end
```

### 6.3 对照组

| 对照 | 角色 | roadmap 锚点 |
|---|---|---|
| **gopher-lua** | **主基准,≥2x 验收对象** | §4「≥2x over gopher-lua」;§7「gopher-lua:P1 的差分基准」 |
| **LuaJ-luac / LuaJ-luajc**(可选) | 校准数据复现(Java,需 JVM) | §1 校准测量 1 的中间档 |
| **LuaJIT**(可选) | 终局参照(C,trace JIT) | §1 校准测量 1 的顶档 |

- **gopher-lua 是硬对照**(同为纯 Go,同机同测,≥2x 是 P1 验收门)。望舒与 gopher-lua 在**同一 Go benchmark 进程**里跑同一脚本(同款列内核形状),直接比 ns/op。
- LuaJ/LuaJIT 是**可选复现**(需 JVM/C 工具链,不进 Go benchmark 主流程)——它们的价值是**复现 roadmap §1 的校准测量**(验证「真 LuaJIT 只比 luajc 快 6%」「per-item 下边界主导」这两个立项论据在当前硬件上仍成立),入库为独立脚本 + 数据(§6.5),不是 P1 CI 门禁。

### 6.4 测量方法:Go benchmark 框架 + ns/op

- **Go testing.B**:`go test -bench` 跑,取 `ns/op`(每次「一调用进 VM 整批迭代」的纳秒数)。
- **≥2x 判据**:`wangshu_ns_per_op ≤ gopher_lua_ns_per_op / 2`,**三档全部满足**才过验收(roadmap §4「全部 ≥2x」)。
- **测量纪律**(减噪):`b.ResetTimer()` 排除编译/建表/建 arena 的一次性成本(只测稳态执行);固定 `GOMAXPROCS`、关 GC 干扰(或 `b.ReportAllocs()` 同时报分配数——望舒数字应**零分配**,gopher-lua 非零,这本身是核心差异的量化);多次运行取中位数,记录硬件(roadmap §1 用 16 核 Xeon 6982P-C,benchmark 记录实际机型)。
- **分配数对照(强信号)**:`b.ReportAllocs()` 让 benchmark 同时报 `allocs/op`。望舒算术/循环档应**接近零分配**(NaN-boxing,[01](./01-value-object-model.md));gopher-lua 因 interface 装箱每次运算分配(roadmap §1/§3、[05](./05-interpreter-loop.md) §3.1)。`allocs/op` 的巨大差距是「为什么望舒快」的直接证据,比 ns/op 更能解释加速来源。

### 6.5 校准数据入库(呼应 doc-gaps「校准测量原始数据未入库」、roadmap §附注)

roadmap §附注:「校准测量的原始数据与方法……留存于发起方仓库的工作区,正式立项时可整理为本项目 `benchmarks/baseline/` 的一部分」。**12 定稿把它落地**:

```
benchmarks/baseline/
  scripts/                  三档脚本(simple/arith/loop.lua)+ Horner(校准同款)
  go/                       Go benchmark(望舒 vs gopher-lua,主验收)
    horner_test.go  simple_test.go  loop_test.go
  calibration/              roadmap §1 校准测量的复现 + 原始数据入库
    README.md               方法:16 核 Xeon 6982P-C、per-item 粒度、ns/op;A/B 隔离说明
    measurement1_horner.md  测量1原始数据表(gopher-lua 729μs / luac 259 / luajc 164 / LuaJIT 154)
    measurement2_dilution.md 测量2:隔离脚本 -37% vs 端到端 ±5-7% 噪声(边界主导稀释)
    luaj/  luajit/          (可选)LuaJ/LuaJIT 复现脚本与运行说明
  results/                  历次运行结果(ns/op + allocs/op + 硬件),供回归对照
```

- **calibration/ 入库的是「立项论据的可复现证据」**——把 roadmap §1 表格里的 729/259/164/154μs 等**原始数据 + 复现方法**固化,任何人可重跑验证「真 LuaJIT 只比 luajc 快 6%」「端到端被稀释到噪声」这两个**整个项目方向所依赖**的测量。这呼应 [架构缺口](../../../llmdoc/memory/doc-gaps.md) 的「校准测量原始数据未入库」。
- **results/ 是性能回归基线**:CI 的基准回归检查(§9)对照 results/ 历史,望舒三档 ns/op 不应回退超阈值。

### 6.6 dispatch spike 的 A/B 口径(收口 05 §2.2)

[05](./05-interpreter-loop.md) §2.2 定:P1 基线用 (a) 大 switch;(b) closure-threading / (c) 预解码是**提速 spike**,采纳口径写进 12。**12 定稿口径(承 05 §2.2):(b)/(c) 必须 ① byte-equal 于 (a) ② 三档脚本不更慢于 (a),两条同时满足才采纳。**

```go
// benchmarks/baseline/go/dispatch_ab_test.go —— dispatch A/B 口径
func TestDispatchSpikeAcceptance(t *testing.T) {
    for _, script := range conformanceCorpus(t) {        // 跑全 conformance 语料
        outA := runWith(t, script, dispatchSwitch)        // (a) 基线
        outB := runWith(t, script, dispatchClosure)       // (b) closure-threading
        outC := runWith(t, script, dispatchPredecoded)    // (c) 预解码
        // 口径①:byte-equal 于 (a)(语义 oracle 唯一性,05 §2.2)
        if outB != outA { t.Errorf("(b) not byte-equal to (a) on %s", script) }
        if outC != outA { t.Errorf("(c) not byte-equal to (a) on %s", script) }
    }
}
func BenchmarkDispatchAB(b *testing.B) {
    // 口径②:三档(simple/arith/loop)上 (b)/(c) 不慢于 (a)
    // 报每档 (a)/(b)/(c) 的 ns/op,人工/脚本判定 (b)/(c) ≤ (a) 才采纳
}
```

- **口径①(byte-equal 于 a)**:(b)/(c) 是 dispatch 优化,**不得改变语义**——[05](./05-interpreter-loop.md) §2.2 纪律「执行逻辑写成可复用 helper,换 dispatch 不改语义」。A/B 用全 conformance 语料跑,任何输出差异都判 (b)/(c) 实现有 bug,**不采纳**。
- **口径②(不更慢)**:dispatch 优化的目的是更快;若 (b)/(c) 在某档反而慢(如 (c) 的 `decodedInstr` 比 `uint32` 大导致 cache 占用翻倍变慢,[05](./05-interpreter-loop.md) §2.1c),则该方案在该档**不划算,不采纳**。三档**都不更慢**才采纳(否则保持基线 a)。
- **为什么口径这么严**:dispatch 是热路径核心,(b)/(c) 引入复杂度([05](./05-interpreter-loop.md) §2.1 分析 Go 下间接 call 与 switch jmp 预测代价相近,净收益主要来自译码外提),**必须用数据证明值得**——byte-equal 保正确、不更慢保有收益,两条都过才把复杂度引入。这与 [05](./05-interpreter-loop.md) §3.4「若实测某档不达 2x,提速顺序是先 (c) 再 (b),而非改值表示」呼应。

---

## 7. 测试与各 tier 演进(前瞻)

P1 建立的三套机制(conformance / 差分 fuzz / 基准)如何复用到 P2-P5(roadmap §4 阶段、§5 原则 2):

| 阶段 | 复用什么 | 新增什么 | 差分作为主防线的作用 |
|---|---|---|---|
| **P2 分层桥** | conformance + 差分 fuzz 全套 | **IC 反馈正确性**:IC 记录的类型 feedback([05](./05-interpreter-loop.md) §6.4)是否真实反映运行期类型(feedback 错会误导 P4 投机) | IC 反馈是「旁路记录」,不改输出 → 差分仍是「望舒 vs gopher/官方」;新增「IC 记录 vs 实际类型」的白盒断言 |
| **P3 Wasm 层** | 差分 harness 的 Runner 抽象(§3.8) | **同一 Proto 走 crescent vs gibbous-wasm 输出 byte-equal**([architecture](../architecture.md) §4 不变式 2 最终形态) | **首次出现「同 Proto 不同层」差分**——比 P1「不同实现」差分更强(无实现差异噪声);Wasm 编译器 bug 靠它逐字节抓 |
| **P4 method JIT** | P3 的同-Proto 差分 + GC 压力 fuzz | **deopt 正确性**:IC 投机失败时 OSR exit 回解释器,exit 后状态必须与「一路解释」一致 | deopt 是 JIT 最危险点——投机的 f64 快路径若 guard 漏判,会**静默产错果**;「投机路径 vs 解释器」逐字节差分是**唯一**能抓住它的手段(roadmap §5 原则 2 点名) |
| **P5 trace JIT** | 全套 + 同-Proto 差分 | **trace 投机 + snapshot/deopt 正确性**:trace 录制的假设(循环不变量、类型稳定)若被运行期打破,snapshot 恢复必须 byte-equal | trace JIT 的护城河也是其最危险处——CSE/循环不变量外提/分配下沉等优化任一错误都静默错果;**持续 fuzz「trace vs 解释器」是主防线**(roadmap §5);P1 建的 harness 此时价值最大化 |

> **核心前瞻论断**(roadmap §5 原则 1+2 的合流):**解释器(crescent)永不退役,因为它是所有上层的语义 oracle**——P3/P4/P5 的每个编译层,其正确性判据都是「同一 Proto 走编译层 vs 走解释器,输出 byte-equal」。P1 建的差分 harness(§3.8 的 Runner 抽象)是这条主防线的**物理载体**:P1 时它跑「望舒 vs gopher/官方」,P3+ 时它跑「望舒解释器 vs 望舒编译层」。**JIT 最危险的 bug(投机错误静默错果)无法用有限用例覆盖,只能靠『编译层 vs 解释器』持续逐字节差分撞出**——这就是为什么 roadmap 把它列为五条贯穿原则之一,也是为什么 P1 现在就要把 harness 建对(而非等 P3 再补)。

---

## 8. CI 门禁(收口 architecture §4 不变式 2)

[architecture](../architecture.md) §4 不变式 2:**「层间逐字节差分是 CI 必过门禁」**。本节定**门禁逻辑**(测什么、什么必过);workflow/job/hooks 等**机制载体**在 [engineering](../engineering.md)(其 §3.1 的 ci.yml 与本节五步一一对应)。P1 的 CI 流程:

```
每个 PR 触发:
  1. 单元测试       go test ./internal/...                    必过(含 codegen 黄金字节码 §2.5)
  2. conformance    go test ./test/conformance/...            必过(对拍 golden,§2.4)
  3. 差分 fuzz      go test ./test/difftest/ -fuzztime=Ns     必过(一轮固定时长 fuzz,§3)
       - 三方比对(望舒 vs gopher vs 官方,§3.3)零未豁免差异
       - GC 压力 fuzz(§5)零崩溃 + 透明性
       - 失败则最小化复现写进 artifact(§3.6)
  4. 基准回归       go test -bench ./benchmarks/baseline/go/   不回退(对照 results/ §6.5)
       - 三档仍 ≥2x over gopher-lua(roadmap §4 验收)
       - ns/op 不回退超阈值;allocs/op 不增(望舒应零分配)
  5. dispatch A/B(若改了 dispatch) byte-equal + 不更慢(§6.6)
```

**门禁分级**:
- **硬门禁(必过,阻塞合并)**:单元、conformance、差分 fuzz(零未豁免差异 + 零崩溃)、基准 ≥2x。差分 fuzz 是 [architecture](../architecture.md) §4 点名的**必过门禁**。
- **每 PR 的 fuzz 是「固定时长一轮」**(`-fuzztime`,如几十秒到几分钟),保证 PR 反馈速度;**持续 fuzz**(roadmap §5「持续 fuzz」)由**独立长跑任务**(nightly / 专用 fuzz 机)承担——长时间随机撞角落,撞到的失败用例最小化后回流成 conformance(§3.6)。两者分工:PR 门禁防回归,长跑 fuzz 拓新。
- **golden / 豁免清单改动高亮**(§4.8):golden 文件、`exemptions.go` 的 diff 在 PR review 里显眼,防「改 golden / 加豁免来掩盖真 bug」。

> **为什么差分 fuzz 必须是硬门禁**(而非「跑跑看」):roadmap §5 原则 2 把它定为「主防线」,[architecture](../architecture.md) §4 把它定为「必过」。若差分只是 advisory(可失败可合并),则「投机错误静默错果」会随 PR 渗入主干而无人察觉(它不崩溃、不报错,只是结果悄悄错)。把它设为**阻塞合并的硬门禁**,是把「逐字节一致」从口号变成机制。这也是 P1 验收(roadmap §4「与 gopher-lua 差分 fuzz 输出逐字节一致」)的 CI 兑现。

---

## 9. 不变式清单(实现与测试须守)

1. **官方 Lua 5.1.5 是最终 oracle**:任何语义疑义,官方输出为准;gopher-lua 是同生态参照 + 性能基准,gopher 偏离官方处以官方为准并豁免 gopher(§3.3/§4.7)。
2. **可观察输出 = O1..O5**(§3.1):print/io.write 字节流、返回值、错误(值+位置+traceback)、副作用序、退出态。其余是 VM 内部状态,差分不比。
3. **差分逐字节一致是 CI 必过门禁**([architecture](../architecture.md) §4 不变式 2、roadmap §4 验收)——硬门禁,阻塞合并。
4. **严格口径项绝不豁免**:`pairs` 严格用例(键集确定)、`%.14g`/format、错误措辞(非本质不可控项)、寄存器分配同构(§2.5)、GC 透明性(§5)。豁免只给本质不可比项(地址/random/GC 数值/locale/libc 文本)且集中审计(§4.8)。
5. **基准必须用列内核形状**(§6.1,前提一):一次 CallFn 进 VM 后整批迭代,非 Go 侧 per-item 反复跨界——否则边界成本主导,测不出 VM 加速。
6. **三档全部 ≥2x over gopher-lua**(roadmap §4):simple/arith/loop 任一档不达标则 P1 验收不成立。
7. **dispatch 优化采纳口径**:(b)/(c) 必须 byte-equal 于 (a) 且三档不更慢(§6.6,收口 05 §2.2)。
8. **GC 压力 fuzz 双目标**:高频 GC 下①输出与正常 pacing byte-equal(透明性)②不崩溃(§5,收口 06 §11)。
9. **fuzz 失败固化为 conformance**:撞出的差异最小化后入库为确定性用例(§3.6),回归不依赖再次随机撞中。
10. **JIT 主防线复用 P1 harness**:P3+ 的「同 Proto 走编译层 vs 解释器 byte-equal」复用 §3.8 的 Runner 抽象——解释器永不退役,是所有上层的 oracle(roadmap §5 原则 1+2)。

---

## 10. 验收口径总表(本文最有价值的产出 —— 逐条收口)

下表是**每个被各 P1 文档指向 12 的口径问题 → 12 的最终决策**。这张表是 12 存在的核心理由:它把散落在各文档的「待 12 定」一次性收口。

| # | 口径问题 | 提出文档(节) | **12 的最终决策** | 理由 / 机制 |
|---|---|---|---|---|
| 1 | **pairs/next 遍历序**是否逐字节一致 | 01 §8、02 §10、06 §9.3/§11、07 §11、10 §13.1 | **混合口径**:键集确定 → **严格逐字节**;遍历序本质未定义 → **排序后比较** | 严格口径用满 06 §9.3 锁死的 JSHash 哈希一致性探针(最大化差分强度);仅本质未定义项豁免挡假阳性(§4.1) |
| 2 | **数字→字符串 `%.14g`**(tostring/CONCAT) | 05 §4.6/§13、03 §11、07 §730 | **必须逐字节**,复刻 C `printf("%.14g")` | 笛卡尔积用例对拍官方;折叠与运行期共用同一 `NumberValue`+格式化函数天然一致(§4.2) |
| 3 | **string.format `%f/%g/%e/%q`** | 10 §13.1、05 §13 | **严格逐字节**,复刻 C printf / addquoted | Go fmt 与 C printf 有微差,需校准;format×浮点笛卡尔积对拍(§4.2) |
| 4 | **tostring(table/func/thread/ud) 地址** | 07 §11/§982、10 §13.1 | **豁免**:正则脱敏地址为 `0xADDR`,保留类型前缀 | arena 偏移 ≠ C 指针 ≠ Go 指针,本质不可比;脱敏后类型仍被差分(§4.3) |
| 5 | **math.random 序列** | 10 §13.1/§8.4 | **豁免**:生成器禁产 random;conformance 只验范围 | Go rand ≠ C rand,确定 seed 序列也不同(§4.4) |
| 6 | **collectgarbage("count")/gcinfo 数值** | 10 §13.1/§4.6 | **豁免**:数值脱敏,只验类型 | arena 内存模型 ≠ C 堆(§4.5) |
| 7 | **os.date locale 字段** | 10 §13.1/§9.2 | **部分豁免**:月/星期名脱敏,数值字段严格 | 纯 Go 无 setlocale ≠ C locale(§4.6) |
| 8 | **错误措辞**(冠词/单复数/标点/got no value) | 09 §9.3/§862、03 §11、04 §9、07 §1127、08 §189、10 §13.1 | **P1 目标逐字节对齐官方**(移植 5.1 消息模板);本质不可控项(libc 文本/gopher 偏差)进豁免清单 | 措辞对齐是「真移植 5.1 语义」的强信号 + 真实脚本用 match 错误做控制流(§4.7) |
| 9 | **错误类型 / 位置 / traceback 行号** | 09 §314/§673、08 §894 | **严格一致**(类型) + **严格逐字节**(位置/行号/`[C]:`/`(...tail calls...)`) | 类型是语义、位置是 5.1 明确行为;多行脚本钉死行号(§4.7) |
| 10 | **GC 透明性**(GC 改不改可观察行为) | 06 §11 | **必须 byte-equal**:同脚本「正常 pacing vs 高频 GC」输出一致 | GC 是内部状态,不得泄漏可观察面;GC 压力 fuzz 验证(§5) |
| 11 | **GC 压力 fuzz**(漏 push shadow stack / mark 漏扫) | 06 §11/§6.3/§5.2、10 §13.3 | **定稿机制**:GCPAUSE 设极小(每分配即 GC)反复跑,验①透明②不崩 | 把偶发 bug 变确定 bug(高频 GC 必撞持有窗口);stdlib 分配类重点轰炸(§5) |
| 12 | **finalizer 顺序** | 06 §10/§11 | **严格**(创建逆序)对拍官方;多次终结依赖项标 P1 限制豁免 | 确定性用例 print 标识断言顺序(§5.4) |
| 13 | **寄存器分配是否与官方 luac 同构** | 04 §1.1/§10、02 §8 | **黄金字节码单元测试**钉死(对官方 luac);与 gopher 运行期差分**不要求** Proto 同 | codegen 偏差在单元层提前暴露;gopher opcode 不同只比最终输出(§2.5) |
| 14 | **常量折叠边界**(`1/0`/`0/0`/`2^63` 折叠 vs 运行期) | 03 §11、04 §13 | **逐字节同结果**:折叠走与解释器同一 `value.NumberValue`(含 canonicalize) | 同一函数只此一份,折叠与运行期都调它,天然一致(§4.2) |
| 15 | **dispatch A/B 采纳口径** | 05 §2.2/§13 | **(b)closure-threading/(c)预解码必须 byte-equal 于 (a) 且三档不更慢**才采纳 | byte-equal 保正确(语义 oracle 唯一)+ 不更慢保收益(§6.6) |
| 16 | **pattern 匹配**(find/match/gsub/gmatch) | 10 §13.1/§6.6 | **严格逐字节**(移植 5.1 lstrlib) | 算法可复刻无外部因素;(s,pattern) 笛卡尔积对拍(§4 总表、§2) |
| 17 | **table.sort 稳定性**(相等元素排列) | 10 §13.1/§7.6 | **严格**(移植 5.1 auxsort) | 含相等元素数组 + 各种比较器对拍最终排列 |
| 18 | **fmod vs `%`**(负数结果) | 10 §13.1/§8.3 | **严格各自一致**:`%`=`a-floor(a/b)*b`、`fmod`=C fmod | 两者语义不同,各自对拍官方(05 §4.1 MOD) |
| 19 | **数字 coercion 边界**(tonumber/算术/for 接受的串) | 05 §13、07 §5.2/§545、10 §13.1 | **严格**:`parseLuaNumber` 三处共享同一函数,边界串对拍 | 算术/for/tonumber 共用,逐字节一致(07 §545 已归并);边界串笛卡尔积对拍 |
| 20 | **库存在性**(5.1 有的存在 / 5.2+ 的不存在) | 10 §12.3 | **严格**:遍历 5.1 库函数名验存在 + 5.2+ 名验 nil;P1 缺口库标豁免 | 多/少一个函数都是差分失败;P1 未实现库(package 等)标已知豁免(§4.8) |
| 21 | **mono IC 是否够用**(P1 是否需 polymorphic) | 05 §13/§6.3 | **P1 只做 mono**;够不够由**基准实测**(三档 ≥2x 即够),不够才 P2 加 | 多数列内核负载是 mono(05 §6.3);polymorphic 留 P2 反馈驱动 |
| 22 | **step==0 数值 for** | 05 §13/§10.1 | **保持 5.1 不报错行为**(可能死循环),不友好报错 | 报错会与官方差分不一致(官方不报错);差分一致优先(05 §13 倾向) |
| 23 | **lexer 标识符 locale 字节** | 03 §11/§13 | **ASCII-only 口径锁定**:生成器只产 ASCII 标识符;gopher 高位字节偏差豁免 | locale 无法稳定对拍,ASCII-only 是可复现最小公约(03 §11);非豁免而是口径(§4.6) |
| 24 | **opcode 特化变体**(GETTABLE_N/_S 等) | 02 §10 | **P1 不做**(保持 5.1 最小集);若做须 byte-equal 于基线 opcode | 特化是提速 spike,采纳口径同 dispatch A/B(byte-equal + 不更慢,§6.6) |
| 25 | **weak table / ephemeron 语义** | 07 §1081 | **P1 简化(键活则值无条件标活)**;触及精确 ephemeron 的用例标 P1 限制豁免 | P1 范围裁剪;真实嵌入负载罕用 ephemeron(§4.1 cleartable 归本质未定义) |
| 26 | **ColInt64 超界报错**(`\|v\| > 2^53` 抛错,望舒扩展) | 11 §3.3.2(经评审定稿) | **登记为望舒扩展行为**:官方/gopher 无 arena ABI 故无对应;错误措辞 `int64 column value out of exact range` 由本表锁定,差分豁免(arena 路径不参与三方差分) | 宁报错不错果(原则 2 精神):静默丢精度对 ID 类数据 = 静默错果且差分测不出 |

**收口统计**:本表收口 **26 条**口径问题(25 条来自各文档「待 12 定」标注 + 1 条评审新增),覆盖 01/02/03/04/05/06/07/08/09/10/11 全部 P1 文档。其中**严格逐字节**类 11 条(pairs严格部分/format/pattern/sort/措辞/位置/coercion/库存在性/折叠/fmod/finalizer)、**豁免/部分豁免**类 7 条(地址/random/GC数值/date/ephemeron/libc文本/gopher偏差)、**口径锁定 + 机制定稿**类 7 条(pairs混合/GC透明性+压力fuzz/dispatch A-B/寄存器同构/mono IC/step0/ASCII)。

---

## 11. 文档缺口 / 待决(仍未定的口径,记入 memory/doc-gaps)

- **gopher-lua 已知偏差的完整清单**:§3.3/§4.7 说「gopher 偏离官方处以官方为准并豁免」,但**gopher-lua 与官方 5.1 的全部已知差异**需实现期实测建立(它是独立实现,差异点未穷举)。当前缺口:首次跑三方差分时会暴露这些点,逐条入豁免表 + 注明是 gopher 偏差。
- **官方 5.1.5 参照平台的 libc 依赖项**:§4.2 的 NaN/Inf 文本、§4.7 的 libc errno 文本依赖官方编译时的 libc。**未定**:golden 锁的是哪个平台/libc 的官方 5.1.5 输出(Linux glibc?)——需固定一个「金标准官方二进制」并记录,否则不同平台的官方输出本身就不一致。
- **fuzz 有效脚本率与覆盖度量**:§3.7 生成器追求「高有效脚本率」,但**未定**如何度量 fuzz 的覆盖(opcode 覆盖?语法产生式覆盖?)——P1 可先不量化(纯随机时长驱动),P2+ 引入覆盖引导(coverage-guided,如 go-fuzz 风格)提升撞角落效率。记缺口。
- **最小化(shrinking)的语义保持**:§3.6 的 reductions 删语句/简化常量后需「仍触发差异」,但删改可能**改变脚本是否合法 / 是否仍在 P1 子集**——`isValidP1` 校验已挡非法,但「最小化后触发的是同一个 bug 还是新差异」无保证。P1 接受「最小化到能复现即可」,精确「同根因」判定留缺口。
- **基准的跨硬件可比性**:§6.4 记录硬件,但 ≥2x 验收在不同 CPU 上**倍率可能波动**(cache/分支预测差异)。**未定**:验收基准锁定在某参照机型,还是「任意机型上 gopher 与望舒同机比 ≥2x」(后者更鲁棒,因同机对照抵消硬件差异)——倾向后者(同机对照),记决策待确认。
- **LuaJ/LuaJIT 复现的工具链依赖**:§6.3/§6.5 的 calibration 复现需 JVM(LuaJ)与 C 工具链(LuaJIT),**非纯 Go**,不进 CI。**未定**:这些复现是「立项时跑一次入库数据」还是「周期性重跑验证论据仍成立」——P1 倾向前者(一次性固化原始数据),记缺口。
- **编译期错误的位置粒度**:§3.2 把编译错误措辞+位置纳入差分,但**望舒自定义 opcode + AST 双遍**([04](./04-frontend-parser-codegen.md) §1)可能让某些编译错误的**行号/列号**与官方单遍 luac 略有偏差(报错时机不同)。**未定**:编译错误位置是否要求与官方逐字节一致,还是只要求「类型 + 行号」一致(放宽列号)——倾向「类型+行号严格,列号宽松」,待 [04](./04-frontend-parser-codegen.md) 错误位置实测后定。
- **co-routine 跨 resume 的差分边界**:[08](./08-coroutines.md) 的协程状态转移措辞已指向 12,但**协程的可观察输出**(yield/resume 间的 print 交织顺序、跨协程的副作用序)在 O4(副作用序)下的精确比对粒度,需 [08](./08-coroutines.md) 配合实测确认(P1 协程是 host→Lua 重入,副作用序应确定,但未验)。记缺口。

---

相关:[01-value-object-model](./01-value-object-model.md)(字符串哈希 / pairs 序口径来源 §8) ·
[02-bytecode-isa](./02-bytecode-isa.md)(opcode 特化变体 §10 / 语义对齐 5.1) ·
[03-frontend-lexer](./03-frontend-lexer.md)(词法错误措辞 / locale ASCII-only / 数字折叠) ·
[04-frontend-parser-codegen](./04-frontend-parser-codegen.md)(寄存器分配同构 §1.1/§10 / 常量折叠边界 / 编译错误措辞) ·
[05-interpreter-loop](./05-interpreter-loop.md)(三档 ≥2x §3 / dispatch A-B 口径 §2.2 / %.14g §4.6 / mono IC §13) ·
[06-memory-gc](./06-memory-gc.md)(pairs 序哈希环锁定 §9.3 / GC 压力 fuzz §11 / finalizer 顺序 §10) ·
[07-metatables-metamethods](./07-metatables-metamethods.md)(tostring 地址豁免 §11 / coercion 共享 §5.2 / 措辞 §11) ·
[08-coroutines](./08-coroutines.md)(协程状态/措辞核对) ·
[09-errors-pcall](./09-errors-pcall.md)(错误措辞 / traceback 行号逐字节 §9.3) ·
[10-stdlib](./10-stdlib.md)(format/pattern/tostring/sort/措辞/库存在性差分敏感总表 §13) ·
[11-embedding-arena-abi](./11-embedding-arena-abi.md)(gopher-lua 差分基准 / drop-in 行为兼容 §9) ·
[architecture](../architecture.md)(§4 不变式 2:层间差分 CI 必过门禁) ·
[design-premises](../../../llmdoc/must/design-premises.md)(前提一列内核负载形状 / 前提三原则 2 差分主防线) ·
`docs/design/roadmap.md` (§1 校准测量 / §4 P1 验收 / §5 原则 2)
