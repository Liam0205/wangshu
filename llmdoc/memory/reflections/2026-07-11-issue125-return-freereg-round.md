---
name: 2026-07-11-issue125-return-freereg-round
description: >
  issue #125 nightly fuzz corpus 暴露的 `return f()or(f())` 层间分歧调查轮
  (2026-07-11,PR #126,closes #125):P1 返 0、auto 返 nil、PUC oracle 说 nil,
  表象是「P1-vs-auto 层间分歧指向升层路径」,真相是共享前端 codegen 存量 bug 让两个
  tier 各自读到不同的栈垃圾。根因:`stmtReturn` 的单值带短路跳转链分支在调用
  `exp2NextReg` 之前捕获 `base = fs.freereg`,而 `exp2NextReg` 内部先 `freeExp`
  (freereg 回落一格)再把值物化到低一格;`RETURN A = base` 因此指向值的后一格,
  读到未定义数据。修复:`RETURN` 与 `freereg` 恢复都改用 `single.info`(`exp2reg`
  实际物化的位置),与上方无跳转链快路径写法一致。修完立刻横向审计 `stmt.go` 里
  另外四处 `base := freereg` 捕获(num-for / gen-for / vararg-return / multi-
  return),都在表达式求值之前捕获、中间不夹 `freeExp` 移动,安全。验证:corpus
  入 `testdata/fuzz/FuzzAutoPromote/b03a5a1dd9e56fbf` 常驻(轻量、budget-bounded,
  符合 unreproducible-crasher-triage guide 位置判据——本轮是该判据的第一次正向
  消费)+ `test/difftest/corners_test.go` 加 5 个 `corner_ret_*` probes(or / and /
  not 变体、带返回值的 or、寄存器基移位)+ 三 build 全绿。核心教训:①「层间分歧
  归因先问 PUC oracle」——差分 fuzz 的 P1 参照系本身可能是错的,层间分歧若两 tier
  都错,bug 在共享前端 / stdlib,不在 tier;② 前端 codegen 的「捕获寄存器水位再用」
  审计判据——capture 与 use 之间若夹了会移动 freereg 的调用(`freeExp` / `exp2NextReg`)
  就危险,表达式物化后的权威位置是 `e.info`,不是预捕获水位;③ what worked:
  `luac -l` 逐指令对照法——同形状让 PUC luac 出字节码,与 wangshu dump 对比,
  RETURN 操作数差异直接指出 bug 所在,比读 codegen 源码猜快得多。
metadata:
  type: reflection
  date: 2026-07-11
---

# issue #125 RETURN 读预捕获 freereg 拿到栈垃圾(2026-07-11,PR #126)

> 范围:分支 `fix/issue125-nightly-crasher`,PR #126。nightly fuzz corpus
> `function sum()for A=0,0 do end end return sum()or(sum())` 暴露 P1-vs-auto
> 层间分歧,追下去发现是共享前端 codegen 的 RETURN 操作数计算存量 bug。

## 任务

nightly fuzz 报差分:corpus `function sum() for A=0,0 do end end return sum() or (sum())`
在 P1 层返 `0`,在 auto(升层)层返 `nil`,PUC luac oracle 说答案应该是 `nil`。
判定是升层路径 bug 还是共享层 bug、定位根因并修复,同时补足回归。

## 期望与实际

- 期望:按「层间分歧 → 升层路径 bug」的直觉,查升层触发点、寄存器传递、栈同步等
  升层专属机制。
- 实际:P1 和 auto **两个 tier 都在读栈垃圾**——只是各自恰好读到的历史值不同
  (P1 读到上一次残留的 `0`,auto 读到 `nil`)。根因在共享前端 codegen 的
  `stmtReturn`,与升层机制无关。差分 fuzz 的 P1 参照系本身是错的,只是错法碰巧
  和 auto 不同,才让分歧显性化。

## 根因

`internal/frontend/compile/stmt.go` `stmtReturn` 的单值带短路跳转链分支
(如 `return f() or (f())`),原代码:

```go
base := fs.freereg
fs.exp2NextReg(s.Line, &single)
fs.emitABC(s.Line, bytecode.RETURN, base, 2, 0)
fs.freereg = base
```

`exp2NextReg` 内部会先 `freeExp(single)`——把 `single` 已经占用的临时寄存器
释放掉,`freereg` 因此回落一格——然后再把值物化到「新的」`freereg` 位置。物化
完成后 `single.info` 指向真正的落位,比外面预捕获的 `base` 低一格。发出的
`RETURN A = base` 因此指向值的后一格,读到栈上的历史残留。

同形状 PUC luac 发 `RETURN 1 2`(值在 R(1)),wangshu 发 `RETURN 2 2`,读 R(2)
的未定义数据。

修复(与上方无跳转链快路径写法一致):

```go
fs.exp2NextReg(s.Line, &single)
fs.emitABC(s.Line, bytecode.RETURN, single.info, 2, 0)
fs.freereg = single.info
```

## 横向审计

`stmt.go` 其余四处 `base := fs.freereg` 捕获(num-for 初值三元组、gen-for 迭代
器三元组、vararg-return、multi-return)都在表达式求值**之前**捕获,`base` 之后
没有夹 `freeExp` 类会移动 `freereg` 的调用就直接用,安全。真正危险的是「capture
→ 夹一次 `freeExp` / `exp2NextReg` → use」的组合,本轮只有 stmtReturn 单值带跳转
链一处踩到。

## 验证

- corpus 入 `testdata/fuzz/FuzzAutoPromote/b03a5a1dd9e56fbf` 常驻回归(轻量 +
  budget-bounded,符合 unreproducible-crasher-triage guide 的位置判据——本轮
  是该判据首次正向消费,与 #123 的重 workload 走 `test/regression/` 形成对照);
- `test/difftest/corners_test.go` 加 5 个 `corner_ret_*` probes:or-chain 无返回值
  (局部函数)、or-chain 无返回值(全局函数,原 corpus)、and-chain、寄存器基
  移位变体、or-chain 有返回值(值传递路径旁证正常);
- 三 build(amd64 / arm64 / darwin arm64)全量绿。

## 调查路径

1. corpus 重放,确认 P1-vs-auto 分歧稳定复现;
2. 手工最小化:局部函数 / 去外层括号变体都保持分歧;赋值形式(`local x = f() or f()`)
   和带返回值变体(`function f() return 3 end`)都正常——说明 bug 只在 `return`
   语句的这条分支;
3. 问 PUC luac oracle:同源答案 `nil`——两个 tier 都错;
4. `luac -l` 对照:PUC 发 `RETURN 1 2`,wangshu dump 发 `RETURN 2 2`,操作数
   `A` 差 1;
5. 顺着 `RETURN` 发射点回溯到 `stmtReturn` 单值带跳转链分支,一眼看出 `base :=
   freereg` 在 `exp2NextReg` 之前捕获;
6. 一行语义修复 + 横向审计四处 sibling 无踩雷 + 5 个 probe 兜住形状。

## 核心教训

### 教训 1(差分信号的归因陷阱:层间分歧不必然是层间 bug)

「P1 vs auto 分歧」是 nightly 差分 fuzz 最常见的报警形式,直觉归因是**升层路径**
(层间字节码 / 寄存器 / 栈同步)出问题。这次真相相反:两个 tier 共享同一个前端
codegen,前端发的 `RETURN 2 2` 让两个 tier 都读到栈上未定义数据;只是 P1 的
栈布局历史让 R(2) 恰好残留 `0`,auto 的升层准备让 R(2) 是 `nil`,分歧才显性化。
**差分 fuzz 的 P1 参照系本身可能是错的**——只是错法不同才被差分捕获;若两个 tier
巧合读到同一份历史残留,这个 bug 会完全静默通过差分。

正确的归因纪律:看到层间分歧,第一步不是查升层路径,而是**问第三方 oracle 拿真值**。
PUC luac 是最便宜的 oracle(同 corpus 直接跑 + `luac -l` 出字节码)。若 oracle
说 P1 也错,共享层(前端 codegen / stdlib / VM 语义)嫌疑最大,升层路径反而
可以先放下。

**Why**:「差分 = 参照系正确 + 被测有 bug」是差分测试的隐含前提。差分 fuzz 的
参照系是「另一层」,不是「已知正确的实现」;两层都错也会显性化(只要错法不同),
两层都对同一份错则会静默。忽视这个前提,归因就会天然偏向「被测层」而放过共享层。

**How to apply**:任何「P1 vs auto / P3 vs P4 / amd64 vs arm64」分歧,第一步跑
PUC luac 或已知正确的第三方看真值;真值和「参照那一层」一致,再归因到被测层;
真值和两层都不一致,共享层优先,`luac -l` 对照字节码是最快的下一步。

### 教训 2(前端 codegen 的「捕获寄存器水位再用」审计判据)

`base := fs.freereg` 这种「把当前寄存器水位记下来,后面拿来当基址发指令」的
写法,危险区域是 **capture 与 use 之间夹了会移动 `freereg` 的调用**——
`freeExp` / `exp2NextReg` / `freereg2 = ...` 等。表达式物化完成后,权威位置是
`e.info`(`exp2reg` / `exp2NextReg` 写回的),不是外面的预捕获水位。

本轮 stmtReturn 就是踩到这个:`exp2NextReg` 内部先 `freeExp` 再物化,`single.info`
落在原 `base - 1`,`base` 已经指向后一格。修法有两种:

- 用 `single.info` 替换 `base`(本轮采用,与同函数无跳转链快路径写法一致);
- 把 `base := freereg` 挪到 `exp2NextReg` **之后**再捕获(等价,但读者更容易
  忽略中间那次 `freeExp` 隐含的水位移动)。

第一种更好,因为它直接消费 `exp2reg` 返回的权威位置,不依赖读者去脑补中间步骤。

横向审计判据:凡是 `base := fs.freereg`(或等价的水位缓存)与 use 之间夹了任何
可能移动 `freereg` 的操作,红旗;要么改用物化后的 `e.info`,要么把 capture 挪
到最后一次移动之后。本轮审计了 stmt.go 另外四处 sibling,都在表达式求值前捕获、
中间无 `freeExp`,安全;后续新写代码套这个判据即可。

**Why**:寄存器水位是隐式状态,`freeExp` 之类的调用副作用不显眼(名字看着像
「释放某个 exp」,读者不一定意识到它同时把 `freereg` 减了);预捕获一个隐式状态,
后面若中间状态被动过,预捕获值就是陈旧的。用 `e.info` 消费返回值等于把隐式状态
显式化。

**How to apply**:review / 写前端 codegen 时,凡见 `base := fs.freereg` 模式,
沿代码流看到 use 点之前,标记每次 `freeExp` / `exp2NextReg` / `freereg--` 类的
调用;有一次就红旗,改用 `e.info` 或后移 capture。

### 教训 3(what worked:luac -l 逐指令对照法)

面对「codegen bug 但不知道在哪个 pass」,让 PUC luac 对同源代码出字节码
(`luac -l -p file.lua`),与 wangshu 的字节码 dump 逐指令 diff,发散点通常
直接锁定生成该指令的 codegen 分支。本轮就是 `RETURN` 操作数 `A` 差 1 一眼看到,
之后回溯到 `stmtReturn` 单值带跳转链分支不到五分钟。

**Why**:PUC luac 是同规范另一个实现,同源代码字节码序列结构一致(局部差异
被文档化),它做参照系比读自家 codegen 源码猜快一个数量级;`luac -l` 输出人
可读,操作数级别的 diff 直接指出「哪条指令的哪个字段错」,回溯到发射点只需要
grep 一个 opcode。

**How to apply**:任何「前端 codegen / 字节码语义」怀疑点,先跑 `luac -l` 对
同源代码出参考字节码,与自家 dump 对齐比对;操作数 diff 到具体字段,再回溯
发射点。

### 教训 4(unreproducible-crasher-triage guide 位置判据首次正向消费)

#123 沉淀的 guide 里区分「轻量 budget-bounded 形状 → `testdata/fuzz/`」vs
「重 workload / 无界递归 → `test/regression/`」,本轮是判据的第一次正向消费:
corpus 是简单的 `function` + `for` 循环 + `return`,budget 天然有界,`testdata/
fuzz/FuzzAutoPromote/b03a5a1dd9e56fbf` 是自然选择,fuzz worker 会把它当种子
mutation 探索周边形状;与 #123 的重 workload corpus 放在 `test/regression/`
形成对照。guide 判据可执行、跑通了。

## Promotion 候选

- **教训 1「层间分歧归因先问 oracle」** 值得进 guide,可能的归属:
  - 并入 `prove-the-path` 的诊断侧(「先证明失败路径」的分诊前置——证明的前提
    是知道真值);
  - 或并入 [[cross-backend-semantic-fix-sweep]] 的分诊前置(那个 guide 目前
    覆盖「一个 backend 修了另一个 backend 也要 sweep」,可以扩到「差分信号
    归因前先拿 oracle 真值」);
  - 或新开一篇「差分信号归因」独立 guide,与 unreproducible-crasher-triage 并列。
  倾向后两者之一;第一条(prove-the-path)侧重「路径已知,证明它跑到」,和
  「路径未知,先定位是哪层」的场景不完全同族。由 recorder 判断。
- **教训 2「前端 codegen freereg capture-use 间距审计判据」** 偏前端 codegen
  专属,频次存疑(本轮 stmt.go 只中一处、其余四处 sibling 都安全)。先留反思,
  等再中一到两次同族 bug 再考虑升格。若升格,归 `internal/frontend/compile/`
  局部 guide 或 codegen 审阅 checklist 里一条。
- **教训 3「luac -l 对照法」** 通用性强、成本低,可以进「前端 codegen 调试手法」
  guide 或 recipes;若无合适 guide,单独留反思也够用。
- **教训 4「triage guide 位置判据首次正向消费」** 只是消费信号,不需要升格,
  但在 unreproducible-crasher-triage guide 关联节挂上本轮反思即可,证明判据
  已跑通。

## 触发场景

- 差分 fuzz 报 P1-vs-auto / P3-vs-P4 / amd64-vs-arm64 分歧,归因升层 / 后端
  路径之前(教训 1:先问 PUC luac 拿真值,两层都错则共享层优先);
- 前端 codegen review / 写新分支时,见 `base := fs.freereg` 或等价水位缓存
  模式(教训 2:capture 与 use 之间任何 `freeExp` / `exp2NextReg` 都是红旗,
  改用 `e.info` 或后移 capture);
- 怀疑前端 codegen bug 但不知落哪个 pass(教训 3:`luac -l` 对同源代码出参考
  字节码,与自家 dump 逐指令 diff,操作数字段级 diff 直接锁定发射分支);
- fuzz corpus 入库位置抉择(教训 4:轻量 + budget-bounded 走 `testdata/fuzz/`,
  重 workload / 无界递归走 `test/regression/`,详见
  unreproducible-crasher-triage guide)。

## 关联

[[2026-07-11-issue123-unreproducible-crasher-round]](本轮消费该轮 guide 的
「corpus 位置判据」——轻量 budget-bounded 形状走 `testdata/fuzz/`,#125 corpus
是判据首次正向消费的例子)· [[cross-backend-semantic-fix-sweep]](教训 2 横向
审计四处 sibling 的做法是该 guide「一处修好后立刻 sweep 同族」纪律在前端 codegen
内的对应物;教训 1 归因纪律可能升格并入该 guide 的分诊前置)· PR #126 ·
issue #125 · `internal/frontend/compile/stmt.go` · `test/difftest/corners_test.go` ·
`testdata/fuzz/FuzzAutoPromote/b03a5a1dd9e56fbf`
