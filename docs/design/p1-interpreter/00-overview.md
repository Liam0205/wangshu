# P1 总览:crescent 解释器(tier-0)——组件依赖 / 实现里程碑 / 验收 / 人月分解

> 状态:**设计阶段,详细设计已齐备**。本文是 P1 文档集(01-12)的导航与施工计划:每篇文档的定位、
> 组件依赖与构建顺序的细化、里程碑验收门槛、人月分解、跨文档定稿决策速查。
> 上游:`docs/design/roadmap.md` (§4 P1)、[architecture](../architecture.md)(§3 依赖图/§5 构建顺序)。
> P1 目标一句话:**简单/算术/循环三档脚本全部 ≥2x over gopher-lua,与官方 Lua 5.1.5 差分 fuzz 输出逐字节一致(gopher-lua 为同生态参照,偏差豁免——12 §3.3)**;
> 止步于此也成立——一个「更好的 gopher-lua」(roadmap §5 原则 3)。

---

## 0. 文档地图:谁定什么(单一事实源分工)

| 文档 | 定位 | 单一事实源(其它文档以它为准) |
|---|---|---|
| [01-value-object-model](./01-value-object-model.md) | **脊柱** | NaN-box 位布局、tag 表、GCRef 寻址、GC 对象内存布局(含 Table gen / Upvalue nextOpen / Proto LocVars 回填) |
| [02-bytecode-isa](./02-bytecode-isa.md) | **脊柱** | 指令编码、opcode 完整表、调用约定、IC slot 结构、RK/常量池 |
| [03-frontend-lexer](./03-frontend-lexer.md) | 前端 | token 枚举与载荷、词法规则(转义/长括号/数字)、LL(2) 前瞻归属(parser 自缓存) |
| [04-frontend-parser-codegen](./04-frontend-parser-codegen.md) | 前端 | AST 双遍决策、expdesc/freereg 寄存器分配(与官方 luac 同构)、跳转回填、编译期错误 |
| [05-interpreter-loop](./05-interpreter-loop.md) | **脊柱** | dispatch 选型(基线大 switch)、frame/CallInfo、reentry 调用模型、IC 执行(per-table 代次)、错误传播(显式返回)、upvalue 关闭 |
| [06-memory-gc](./06-memory-gc.md) | 内存 | arena 分配器、STW mark-sweep、根集合 R1..R9、shadow stack 二元形态、JSHash、弱表 GC 协作、finalizer |
| [07-metatables-metamethods](./07-metatables-metamethods.md) | 语义 | 全部元方法语义、__index/__newindex 链、coercion 归属(parseLuaNumber 一套)、__le→__lt 回退、__mode 弱表语义 |
| [08-coroutines](./08-coroutines.md) | 语义 | 路线 B(单 goroutine + executeSignal 冒泡)、Thread 状态机、yield 跨 C 边界检测 |
| [09-errors-pcall](./09-errors-pcall.md) | 语义 | error/pcall/xpcall、traceback 格式、getobjname P1 简化范围、错误信息目录 |
| [10-stdlib](./10-stdlib.md) | 库 | host 调用约定 helper(luaL_* 等价)、七子库清单与 P1 裁剪表、pattern matcher、shadow stack 库级纪律 |
| [11-embedding-arena-abi](./11-embedding-arena-abi.md) | 公共 API | wangshu.go 门面、arena ABI 字段级 spec(列/字符串区/presence bitmap)、per-item API、句柄表 |
| [12-testing-difftest](./12-testing-difftest.md) | **验收收口** | 测试金字塔、三方差分 harness、验收口径总表(25 条)、GC 压力 fuzz、三档基准 |

阅读顺序建议:实现者先读 01→02→05(三脊柱),再按所做里程碑读对应文档;12 在每个里程碑收尾时查口径。

---

## 1. 组件依赖(回顾)与关键耦合点

依赖图见 [architecture](../architecture.md) §3(无环:`value` 不依赖 `object`,`object` 不依赖 `crescent`)。设计定稿后新增的跨组件**关键耦合点**(实现时最易出错处):

1. **`value.NumberValue` 是 NaN 规范化唯一入口**(01 §3.4):lexer 数字转换(03 §5.4)、codegen 常量折叠(04 §5.5)、算术运算(05 §4.1)、arena ABI float 列读入(11 §4)全部经它——四处共用一个函数,折叠结果与运行期天然一致。
2. **`parseLuaNumber` 是 string→number 唯一入口**(07 §5.2):算术 coercion、数值 for、`tonumber` 共用——三处行为一致由单一实现保证。
3. **字符串 intern 是 codegen↔arena 的桥**(04 §11):字符串常量在编译期 intern 进 arena,`Proto.Consts` 的 GCRef 因此成为 GC 根(06 §5.1 R6)。
4. **Table gen 代次的 bump 纪律**(05 §6.5):rehash / setmetatable / 数组哈希迁移三处必须 bump,改值不 bump——写侧职责分散在 object/crescent,漏 bump = IC 读脏(差分可捕)。
5. **shadow stack 纪律只约束 host**(06 §6):解释器主循环零登记(栈即根);stdlib 每个分配中间对象的函数必须 Push/defer Pop(10 §3 范例)——GC 压力 fuzz 是主防线。
6. **arena backing 注入点**(06 §1.1,承 P3 回填):P1 就要把 backing 分配收口为 `newBacking()`,P3 替换为 wazero memory——**P1 实现期的前瞻义务,只此一条**。

---

## 2. 实现里程碑(细化 [architecture](../architecture.md) §5 的 13 步)

每步可独立编译 + 单测通过再进下一步。「验收」列是该步的完成定义;M 编号供排期引用。

| M | 内容 | 对应文档 | 验收(完成定义) |
|---|---|---|---|
| M0 | 工程地基:go.mod/目录骨架/Makefile/.githooks 三件套/ci.yml/lint/oracle 校验脚本 | [engineering](../engineering.md) §6 | `make all`/`make hooks` 可跑;hooks 拦截生效;lint+test(-race)+fuzz-smoke 三 job 绿 |
| M1 | `arena`:线性内存 + bump/freelist 分配器(backing 经 `newBacking()` 注入点) | 06 §1-§3 | 分配/对齐/扩容/偏移稳定性单测;grow 后旧 GCRef 全部有效 |
| M2 | `value`:NaN-box 编解码 + canonicalize | 01 §3 | round-trip 全类型单测;负 NaN 注入后恒规范;`IsNumber`/`IsCollectable` 边界值表 |
| M3 | `object`:六类对象布局读写 helper(含 Table gen / Upvalue nextOpen / WeakMode) | 01 §4-§5 | 手动分配下逐字段读写单测;布局字数与 06 §1.3 公式一致 |
| M4 | `bytecode`:opcode 枚举、编解码、Proto、int2fb、上限常量 | 02 | 编解码 round-trip;§8 示例字节码手工核对 |
| M5 | `gc`:STW mark-sweep + shadow stack + string intern(JSHash)+ pacing | 06 §4-§9 | 高频 GC 模式下对象图压力单测;intern 表弱可达语义;JSHash 与官方哈希值对拍样本 |
| M6 | `frontend/token`+`lex` | 03 | 全 token 种类单测;错误目录措辞;行号(四种换行)边界 |
| M7 | `frontend/ast`+`parse` | 04 §3-§4 | 文法全覆盖单测;赋值/调用消歧;repeat 作用域;首错即停 |
| M8 | `frontend/compile`:codegen + 寄存器分配 | 04 §5-§11 | **黄金字节码测试**:固定源→固定 Proto;§10 示例与 02 §8 逐字节一致;九类编译期错误 |
| M9 | `crescent` 最小循环:算术/循环/调用,无 GC 介入 | 05 §1-§4、§7、§10 | 三档脚本(12 §6)跑通且结果正确;Lua 递归不增 Go 栈(深递归单测) |
| M10 | IC + safepoint + GC 接入 | 05 §5-§6 + 06 §7 | IC 命中/失效单测(gen bump 后 miss);GC 压力模式三档脚本 byte-equal |
| M11 | 元表/错误/协程:metamethod、pcall/traceback、coroutine | 07、09、08 | 官方语义用例(__index 链/__le 回退/yield 边界/错误目录);xpcall handler 在展开前调用 |
| M12 | `stdlib`:base→string(含 pattern)→table→math→os/io 最小集 | 10 | P1 裁剪表「必做」列全绿;pattern 用例对官方;shadow stack 纪律过 GC 压力 fuzz |
| M13 | `wangshu` 公共 API + arena ABI | 11 | Compile/Program.Call 端到端;arena 列读写 + presence bitmap;per-item API 对标用例 |
| M14 | `test/conformance` + `test/difftest` + `benchmarks` | 12 | **P1 总验收**:三档 ≥2x over gopher-lua;差分 fuzz 逐字节一致(口径总表);CI 门禁就绪 |

> **M0 先于一切**(工程地基:hooks/CI/Makefile,详见 [engineering](../engineering.md),估算 ≤0.25 人月);M1-M5 是「值世界地基」(architecture §5:必须在解释器前完全自洽);M9 故意先于 M10(先跑通无 GC 的快路径,再接 GC——错误隔离)。M14 不是最后才开始:difftest harness 应在 M9 后即搭建,随里程碑增量接入用例(12 §1 金字塔);其 CI 门禁占位 job 在 M0 已建,M5/M9/M14 逐步启用。

---

## 3. 人月分解(roadmap §4:6-9 人月)

按单人全职折算;区间下沿=顺利,上沿=含返工与差分修偏:

| 里程碑段 | 内容 | 估算 |
|---|---|---|
| M1-M5 | 值世界地基(arena/value/object/bytecode/gc) | 1.5 - 2 人月 |
| M6-M8 | 前端(lexer/parser/codegen 寄存器分配同构) | 1 - 1.5 人月 |
| M9-M10 | 解释器主循环 + IC + GC 接入 | 1 - 1.5 人月 |
| M11 | 元表/错误/协程 | 0.75 - 1 人月 |
| M12 | stdlib(pattern matcher 是大头) | 1 - 1.5 人月 |
| M13 | 嵌入 API + arena ABI | 0.5 - 0.75 人月 |
| M14 | conformance/差分/基准 + 验收修偏 | 0.75 - 1.25 人月 |
| 合计 | | **6.5 - 9.5 人月** |

与 roadmap 的 6-9 人月吻合(上沿略超出自 stdlib pattern 与差分修偏的不确定性)。**差分修偏不是尾部活动**:每个里程碑就地对拍,把语义偏差消灭在当步(12 §1)。

---

## 4. 跨文档定稿决策速查(实现前必读)

设计期在多篇文档间协商定稿的关键决策,集中列出防止实现时只读单篇而漏掉:

| 决策 | 定稿 | 出处 |
|---|---|---|
| dispatch | P1 基线大 switch;closure-threading 留 spike(byte-equal 且不更慢才采纳) | 05 §2 |
| 错误传播 | 显式 `*LuaError` 返回(不用 panic/recover);协程接入后泛化为 `executeSignal` 三态 | 05 §9、08 §3.3 |
| 协程 | 路线 B:单 goroutine,resume 加一层 execute,yield 信号冒泡;不开 goroutine | 08 §3 |
| IC 失效 | per-table 单调 gen 代次(globals 是特例);mono IC | 05 §6 |
| 字符串哈希 | Lua 5.1 JSHash 分段采样(否决 FNV-1a,为 pairs 序严格口径创造条件) | 06 §9.3 |
| shadow stack | 二元:Lua 执行现场「栈即根」零登记;host 显式 Push/Pop | 06 §6 |
| pairs 序口径 | 混合:键集确定用严格逐字节,本质未定义用排序豁免 | 12 |
| coercion | `parseLuaNumber` 一套共用(算术/数值 for/tonumber);比较绝不 coerce | 07 §5.2 |
| __le 回退 | 5.1 语义:无 __le 时用 `not __lt(b,a)`(5.4 已删,我们保留) | 07 |
| 弱表 | 语义在 07 §13,GC 协作在 06 §8.4;P1 ephemeron 简化(键活则值无条件活) | 07/06 |
| 前端 | AST 双遍(非官方单遍),寄存器分配算法与 luac 同构;lexer pull 式 + parser 自缓存前瞻 | 04 §1、03 §2 |
| 函数名推断 | P1 做 global/field/method(+local 依赖 LocVars);不做完整 symbexec | 09 §8.3 |
| GC | P1 STW full GC;双白翻转保留;写屏障空实现;pacing=live×200% | 06 |
| 值世界归属 | 动态对象住 arena;Proto/指令流/host 注册表住 Go 堆经整数 ID | 01 §1 |

---

## 5. P1 期间的前瞻义务(为 P2/P3 留口,成本≈0)

P1 不实现分层,但四处「留口」让 P2/P3 成为纯增量(全部已写入对应文档):

1. **IC slot 旁路写**:算术 IC 双计数(numHits/metaHits)、表 IC 的 kind/megamorphic 位——P1 写不读(02 §7、05 §6.4)。
2. **opcode 38..63 预留**:P2 profile 伪指令空间(02 §4)。
3. **arena backing 注入点**(06 §1.1):P3 wazero memory 收养。
4. **CallInfo bit50 gibbous 位**(05 §1.2):P3 跨层帧标记,P1 恒 0。

此外 AST 在 P1 编译后即可丢弃,P2 的可编译性分析复用同一 parser 重新产出(04 §1)。

---

## 6. 风险与未决缺口汇总(详见各文档缺口节与 [doc-gaps](../../../llmdoc/memory/doc-gaps.md))

- **性能风险**:≥2x 的主力是去装箱(05 §3 论证),若三档某档不达标,顺序是预解码→closure-threading,**不动值表示**。
- **差分风险**:`%.14g` 格式、pattern 边角、错误措辞精确标点——12 的豁免清单机制兜底,逐项收敛。
- **stdlib 范围**:io/os/require 按 10 的裁剪表,P1 最小集;官方 conformance 套需按裁剪过滤。
- **ephemeron**:P1 简化与 5.1 微差,若差分触及记已知限制(07 §13.5)。
- **dispatch spike**:closure-threading 的闭包签名与捕获策略,M9 基线通过后再定(05 §13)。

---

相关:[01](./01-value-object-model.md) · [02](./02-bytecode-isa.md) · [03](./03-frontend-lexer.md) ·
[04](./04-frontend-parser-codegen.md) · [05](./05-interpreter-loop.md) · [06](./06-memory-gc.md) ·
[07](./07-metatables-metamethods.md) · [08](./08-coroutines.md) · [09](./09-errors-pcall.md) ·
[10](./10-stdlib.md) · [11](./11-embedding-arena-abi.md) · [12](./12-testing-difftest.md) ·
[../architecture](../architecture.md) · [../p2-bridge/00-overview](../p2-bridge/00-overview.md) ·
[design-premises](../../../llmdoc/must/design-premises.md) ·
[evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md)
