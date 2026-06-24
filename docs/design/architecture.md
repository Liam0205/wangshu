# Wangshu 跨阶段架构总览

> 状态:**设计阶段,设计文档集已齐备**(P1 全卷 00-12 可实现深度;P2/P3 详细设计;P4/P5 架构决策)。
> 本文是详细设计集的根索引与共享词汇基线,定义 Go 包布局、
> 组件依赖、月相 tier 与代码包的映射、P1 构建顺序。战略动机见 [roadmap.md](./roadmap.md)。
> 所有 P1 子系统文档都以本文的包名与组件边界为准。P1 施工计划见 [p1-interpreter/00-overview](./p1-interpreter/00-overview.md)。

---

## 0. 本文档集的地图

战略层(为什么)在 `roadmap.md`;实现层(怎么做)拆分在本目录下。文档间引用用相对路径。

```
docs/design/
  roadmap.md                       战略层:动机/校准测量/演进倍率/非目标/prior art
  architecture.md                  [本篇] 跨阶段总览:包布局/组件图/tier映射/构建顺序
  engineering.md                   工程化机制:Git hooks/CI workflows/Makefile/lint/oracle 供给/发布
  p1-interpreter/                  P1 解释器(tier-0 crescent)详细设计 —— 写到可实现
    00-overview.md                 P1 总览:组件依赖/实现里程碑/验收/人月分解
    01-value-object-model.md       [脊柱] NaN-boxing 位布局 + GC 对象内存布局
    02-bytecode-isa.md             [脊柱] 寄存器式字节码 ISA + 完整 opcode 表
    03-frontend-lexer.md           词法分析器
    04-frontend-parser-codegen.md  语法分析 + 代码生成(寄存器分配)
    05-interpreter-loop.md         解释器主循环 + dispatch + inline cache 执行
    06-memory-gc.md                arena 分配器 + mark-sweep GC + shadow stack
    07-metatables-metamethods.md   元表与元方法语义
    08-coroutines.md               协程 / thread 对象 / resume-yield
    09-errors-pcall.md             错误模型 / pcall / error / 栈回溯
    10-stdlib.md                   标准库清单 + host function 调用约定
    11-embedding-arena-abi.md      嵌入 API + arena ABI 字段级 spec
    12-testing-difftest.md         conformance + 层间差分 fuzz + 基准
  p2-bridge/                       P2 分层桥(子目录 7 文件):热度计数/IC反馈/可编译性分析/fallback
  p3-wasm-tier/                    P3 Wasm 编译层(子目录 10 文件):字节码→Wasm/linear memory/trampoline
  p4-method-jit/                   P4 method JIT(子目录 10 文件):JSC Baseline 风格模板编译+IC投机+OSR exit+双后端
  p5-trace-jit.md                  P5 trace JIT 架构决策(开放式)
```

术语统一查 `../../llmdoc/reference/glossary.md`(列内核/NaN-boxing/arena/月相 tier/deopt 等)。

---

## 1. Module 与顶层目录布局

Module path:`github.com/Liam0205/wangshu`(纯 Go,**禁止 cgo**,保持交叉编译能力——见 roadmap §0)。

```
wangshu/
  go.mod                  module github.com/Liam0205/wangshu  (Go 1.22+)
  Makefile                唯一任务入口(test/lint/fuzz/bench/hooks,engineering.md §1)
  .golangci.yml           lint 配置(engineering.md §5)
  .githooks/              pre-commit / commit-msg / pre-push(engineering.md §2)
  .github/workflows/      ci / nightly-diff-fuzz / nightly-benchmark / release(engineering.md §3)
  scripts/                check-oracle / go-fuzz / fuzz-triage / bench-gate 等辅助脚本
  wangshu.go              公共嵌入 API 门面:Compile / Program / NewState / Arena
  doc.go                  包级文档
  internal/               所有实现细节,外部不可见
    value/                NaN-boxed Value(uint64)、GCRef、tag 编解码
    arena/                自管线性内存([]uint64/[]byte)、bump+freelist 分配器
    gc/                   mark-sweep GC、shadow stack、safepoint、写入屏障接口
    object/               GC 对象布局:String/Table/Closure/Proto/Upvalue/Userdata/Thread
    bytecode/             opcode 枚举、指令编解码、Proto、常量池、IC slot 描述
    frontend/
      token/              token 种类与字面量
      lex/                lexer(源码 → token 流)
      ast/               AST 节点定义
      parse/              parser(token → AST)
      compile/            codegen(AST → bytecode,寄存器分配,常量折叠)
    crescent/             【tier-0】解释器主循环、dispatch、IC 执行、CallInfo 栈
    stdlib/               标准库 host functions(base/string/table/math/os/io/coroutine)
    diag/                 诊断与日志(如 "function promoted to gibbous")
    bridge/               【P2】分层桥:热度计数、IC 反馈、可编译性分析、升降层决策
    gibbous/              【tier-1】
      wasm/               【P3】字节码→Wasm 编译器 + wazero 执行环境
      jit/                【P4】method JIT(amd64/arm64 双后端、OSR exit)
    fullmoon/             【tier-2】
      trace/              【P5】trace 录制 / IR / 寄存器分配 / snapshot+deopt
  cmd/
    wangshu/              REPL / 脚本运行器 CLI(可选,便于手测)
  test/
    conformance/          Lua 5.1 conformance 套(移植自官方 + 自写)
    difftest/             层间逐字节差分 fuzz harness
  benchmarks/
    baseline/             校准测量数据与复现脚本(gopher-lua / LuaJ / LuaJIT A/B)
```

设计要点:

- **公共 API 只在 root package**(`wangshu`),其余全部 `internal/`,杜绝外部依赖实现细节、为重构与升层留自由度。
- **执行层包名用月相**(`crescent`/`gibbous`/`fullmoon`),非执行层基础设施用功能名——这是 roadmap §4「代码与文档统一使用月相命名」的落地;诊断日志因此能输出 `function promoted to gibbous` 这类自释信息。
- **跨 tier 共享的基础设施**(`value`/`arena`/`gc`/`object`/`bytecode`/`frontend`)不属于任何单个 tier,保证「编译层是纯增量」——上一个新 tier 只新增 `gibbous/`/`fullmoon/` 下的发射后端,不动共享层(见 [value-representation](../../llmdoc/architecture/value-representation.md))。

---

## 2. 月相 tier 与代码包映射

| tier | 月相名 | 阶段 | 代码包 | 状态 |
|---|---|---|---|---|
| tier-0 | **crescent**(新月) | P1 | `internal/crescent` | **详细设计齐备(00-12 全卷,可实现)** |
| —(基建) | — | P2 | `internal/bridge` | 详细设计 |
| tier-1 | **gibbous**(凸月) | P3 | `internal/gibbous/wasm` | 详细设计(开工前置 spike 闸门) |
| tier-1 | **gibbous**(凸月) | P4 | `internal/gibbous/jit` | 架构决策 |
| tier-2 | **fullmoon**(满月) | P5 | `internal/fullmoon/trace` | 架构决策 |

注意 **tier 比阶段粗一层**:gibbous 同时覆盖 P3 与 P4;**P2 是基建,不是执行层,无月相**(见 [evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md))。

---

## 3. P1 组件依赖图

P1 内部依赖(箭头 = 依赖方向,A → B 表示 A 依赖 B):

```
                       ┌─────────────┐
   source ──► lex ──► parse ──► compile ──► bytecode.Proto
                       (AST)               │
                                           ▼
                                       crescent (解释器主循环)
                                        │   │   │
              ┌─────────────────────────┘   │   └────────────┐
              ▼                              ▼                ▼
          object (Table/Closure/...)     stdlib(host fn)   gc (mark-sweep)
              │                              │                │
              └──────────────┬───────────────┘                │
                             ▼                                 │
                          value (NaN-box) ◄────────────────────┘
                             │
                             ▼
                          arena (线性内存)
```

底座是 `arena`(物理内存)与 `value`(值编码);`object` 在其上定义对象布局;`gc` 管理 arena 内对象生命周期;`bytecode` 定义指令与 Proto;前端三件套产出 Proto;`crescent` 把这一切跑起来;`stdlib` 以 host function 注入。

**无环依赖**:`value` 不依赖 `object`(只认 GCRef=偏移),`object` 不依赖 `crescent`。这保证未来 `gibbous`/`fullmoon` 能复用 `value`/`object`/`arena` 而不被解释器实现绑死。

---

## 4. 三条贯穿全架构的不变式

源自 roadmap §5,在代码层面的约束(详见 [design-premises](../../llmdoc/must/design-premises.md)):

1. **解释器(crescent)永不退役** —— 是所有上层的 deopt 着陆点与语义 oracle。任何 tier 的 `Proto` 必须始终保有可解释执行的字节码。
2. **层间逐字节差分** —— `test/difftest` 对同一 Proto 在不同 tier 上执行,输出必须 byte-equal;这是 CI 必过门禁。
3. **值表示一次定死** —— `value` + `object` + `arena` 的 ABI 是第 1 天承诺,后续 tier 只增不改(见 §1 "纯增量")。

---

## 5. P1 实现构建顺序(自底向上)

每一步都应可独立编译 + 单测通过,再进入下一步。详细里程碑见 [p1-interpreter/00-overview](./p1-interpreter/00-overview.md)。

```
1. arena            线性内存 + 分配器 + 单测(分配/对齐/扩容)
2. value            NaN-box 编解码 + 单测(round-trip 全类型)
3. object           对象布局读写 helper(尚无 GC,手动分配)
4. bytecode         opcode 枚举 + 指令编解码 + Proto 序列化
5. gc               mark-sweep + shadow stack(object 之上)
6. frontend/lex     lexer + token 单测
7. frontend/parse   parser + AST 单测(对拍官方 luac AST 形状)
8. frontend/compile codegen + 寄存器分配(产出可被 dump 的 Proto)
9. crescent         解释器主循环(先跑通无 GC 的算术/循环)
10. stdlib          base 库 → 逐步补齐 string/table/math/...
11. 元表/协程/错误   metamethod、coroutine、pcall
12. embedding       Compile/Program.Call + arena ABI
13. test/difftest   与官方 5.1.5 差分 fuzz(gopher-lua 为参照),锁定验收
```

关键:**第 1-5 步是值世界地基**,必须在写解释器循环前完全自洽——这正是「第 1 天的架构承诺」的工程含义。

---

相关 llmdoc:[design-premises](../../llmdoc/must/design-premises.md) ·
[value-representation](../../llmdoc/architecture/value-representation.md) ·
[evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md) ·
[embedding-contract](../../llmdoc/reference/embedding-contract.md)
