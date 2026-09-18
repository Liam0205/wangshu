---
name: 2026-09-19-issue264-check-conflict
description: >
  巡检处理 #264(由 #262 全范围终审的行号 dump 顺带发现,基线就有,不是 nightly seed):多目标赋值里后面的局部
  变量目标会先于前面的索引目标被写(store 从右往左),`a.x, a = 1, 2` 对数字 2 做索引、`a[b], b = 1, "z"` 用了新键。
  PUC `lparser.c` 的 `check_conflict` 在解析到局部变量目标时回头看已解析的索引目标,表 / 键寄存器与它相同就先
  `MOVE` 一份到新寄存器、让那些目标改用拷贝;望舒 `stmtAssign` 没有这一步。修法照抄(十几行),编译期 pin 一条
  拷贝 + SETTABLE 读拷贝 + 局部目标在前不拷贝,e2e 十条与 `lua5.1` 比对,#262 的 dump 比对脚本补六条冲突模板。
  一条教训:**行号扫描的 opcode 序列差异桶里不只藏行号问题,也藏语义问题**——这一格一直在 #262 的 OPSEQ 桶里
  (luac 多一条 MOVE),#262 的三轮独立审阅都把它归为「基线就有的 opcode 差异」略过,直到第一轮全范围终审者拿例子跑了一下。
metadata:
  type: reflection
  date: 2026-09-19
---

# 多目标赋值的 check_conflict(2026-09-19,issue #264)

> 范围:issue #264,分支 `fix/issue264-check-conflict`。改动只在 `internal/frontend/compile/stmt.go` 的
> `stmtAssign`(解析局部变量目标时的冲突检查 + 拷贝);新测试 `internal/frontend/compile/issue264_check_conflict_test.go`
> 与 `test/regression/issue264_check_conflict_test.go`。

## 任务

不是 nightly seed,是 #262 全范围终审的审阅者在行号 dump 比对时顺手跑了两个例子发现的:

```lua
local t = {} local a = t   a.x, a = 1, 2      -- lua5.1: t.x == 1, a == 2;望舒:attempt to index local 'a' (a number value)
local t = {} local a, b = t, "k"   a[b], b = 1, "z"   -- lua5.1: t.k == 1;望舒:t.k == nil
```

基线 `6e1ce70` 同样如此,与 #262 的行号改动无关,所以当时只记进 doc-gaps、开了 #264,本轮处理。

## 过程

版本核对没有 nightly run 可比;在当前 master(`f680863`)上用 e2e 表直接重放,10 条里 4 条失败(其余是「不该拷贝」
的对照组),现象与 issue 一致。

根因一句话:多目标赋值的 store 从右往左做(PUC 递归 `assignment` 的返回路径也是),所以**后面的局部变量目标先被写**;
前面的索引目标如果把这个局部当表或键,存的时候读到的已经是新值。PUC 的解法是 `check_conflict`:每解析到一个
`VLOCAL` 目标,回头遍历此前所有 `VINDEXED` 目标,`info`(表寄存器)或 `aux`(键 RK)等于该局部寄存器就改指
`fs->freereg`,最后若有命中就 `MOVE freereg, local` 并 `reserveregs(1)`——只发一条拷贝,所有冲突目标共用。

望舒 `stmtAssign` 先把所有目标解析成 `target{tableReg, keyRK}` 数组再统一 store,结构上正好方便:在 `eLocal` 分支
里回头扫 `tgts[:i]`,命中就把 `tableReg` / `keyRK` 改成 `fs.freereg`,最后发一条 `MOVE` 并 `reserveRegs(1)`。拷贝的行
取该局部目标的行(PUC 此时 lastline 就是刚消费的这个 NAME)。注意方向只往回看:`a, t.x = 1, 2` 里局部目标在前,
后面的索引目标读的是 store 之前的值,不需要拷贝——测试里放了这一条对照,防止下次有人把检查写成双向。

`keyRK` 是 RK 编码,常量(≥ 256)永远不会等于寄存器号,所以不用特判;PUC 的 `aux` 同样直接比。

## 验证

- 编译期 pin:6 条源码,数「RHS 之前从该局部拷出的 MOVE」条数(冲突 1 条 / 无冲突 0 条)与每条 SETTABLE 的 (A, B)
  操作数,期望值来自 `luac5.1 -p -l`;不 pin 完整 opcode 序列,因为「末常量 luac 直接 LOADK 进目标寄存器、我们
  LOADK+MOVE」是基线就有的差异,与本 issue 无关。
- e2e 10 条(表寄存器冲突 / 键寄存器冲突 / 同一局部既是表又是键 / 两个索引目标共用 / 冲突目标不在末位 / 局部目标在前
  不冲突 / 不同局部不冲突 / 调用 RHS / 闭包体内),期望值逐条用 `lua5.1 -e` 跑过;两组测试在 master worktree 上都失败。
- #262 的 dump 比对脚本(`.code-review/closed-262/sweep/linecmp.py`)加六条冲突模板,12669 形状行号 0 差异;新模板
  产生的 885 个 opcode 序列差异全部是「望舒多一条 MOVE」这一种(末常量),luac 的 check_conflict MOVE 我们现在同样发。
- frontend / regression / language / conformance p1,frontend / regression / difftest p3 p4,171 条 oracle 语料,
  官方 Lua 套件 p1 全绿;gofmt / vet / lint 干净。

## 教训

### 「opcode 序列不同」的桶里也可能藏语义 bug,不只是行号

#262 的行号比对脚本把 opcode 序列不同的形状单独归一桶、不比行号(后来改成按 opcode 比行号多重集)。#262 反思
§5 记的三轮独立审阅都看过这个桶,都把它归为「基线就有的 codegen 差异(多发 LOADNIL、常量折进 RK……)」——**归类
正确,但没有问一句「这条多出来 / 少掉的指令会不会让结果不同」**。第一轮全范围终审者拿其中一个形状写了个例子跑,
才发现少的那条 MOVE 是语义。

**判据**:与参照实现比对 dump 时,对每一类 opcode 序列差异至少构造一个**能观察结果**的输入跑一遍(不是只看
opcode 名),确认它是纯形式差异(寄存器分配 / 常量折叠)还是有语义。`local a` 多发 LOADNIL 是纯形式;少一条
`MOVE` 在「后面会覆盖这个寄存器」的语句里就是语义。自查办法:差异桶分类表每一行加一列「构造了什么输入证明无语义
差异」,空着的行还没判定。这是 [[cross-backend-semantic-fix-sweep]]「参照实现能本地调用时用 dump 比对表批量扫」
那一节的补充:扫出来的桶要逐类问语义,不只问行号。

## Promotion 决策

- 教训 → [[cross-backend-semantic-fix-sweep]] dump 比对一节末尾补一句「opcode 序列差异桶逐类构造可观察输入判语义」。
- doc-gaps「多目标赋值缺 check_conflict」条目移入已收口。
