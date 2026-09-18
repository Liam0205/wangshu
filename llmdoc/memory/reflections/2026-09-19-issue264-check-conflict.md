---
name: 2026-09-19-issue264-check-conflict
description: >
  巡检处理 #264(由 #262 全范围终审的行号 dump 顺带发现,基线就有,不是 nightly seed):多目标赋值里后面的局部
  变量目标会先于前面的索引目标被写(store 从右往左),`a.x, a = 1, 2` 对数字 2 做索引、`a[b], b = 1, "z"` 用了新键。
  PUC `lparser.c` 的 `check_conflict` 在解析到局部变量目标时回头看已解析的索引目标,表 / 键寄存器与它相同就先
  `MOVE` 一份到新寄存器、让那些目标改用拷贝;望舒 `stmtAssign` 没有这一步。修法照抄(十几行),编译期 pin 一条
  拷贝 + SETTABLE 读拷贝 + 局部目标在前不拷贝,e2e 十条与 `lua5.1` 比对,#262 的 dump 比对脚本补六条冲突模板。
  一条教训:**扫描模板要让参照实现里的每一个 `if` 各为真一次,包括被调用 helper 内部、比较两个目标之间关系的那种**
  ——#262 的三轮独立审阅模板里没有局部变量作多目标赋值目标的形状;终审补的七条局部目标模板里三条进了
  `check_conflict`,却没有一条让它内部「此前索引目标与本局部共用寄存器」的分支为真,所以这一格从未进过扫描面,
  是终审者手写例子跑出来的。补上冲突模板后 dump 比对能抓到它(luac 的拷贝 MOVE 在局部目标名那一行、望舒的 store
  MOVE 在语句末行,跨行形状按 opcode 比行号多重集就不一致),但单行例子里两条 MOVE 数量相抵、只差顺序,按 opcode
  名和条数看不出——桶里的差异要逐类构造可观察输入判语义。
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
- #262 的 `luac5.1 -p -l` 逐指令比对脚本(本地审计的临时工具,未入仓;做法见 [[cross-backend-semantic-fix-sweep]])加六条冲突模板
  (五条冲突 + 一条「局部目标在前、不冲突」对照),12669 形状行号 0 差异;新模板产生的 1436 个 opcode 序列差异里,四条常量
  收尾的冲突模板全是「望舒多一条 MOVE」(末常量 LOADK+MOVE,基线就有),对照模板全是「望舒多一条 LOADK」(同一根因的另一种
  表现),调用 RHS 那条 0 差异;luac 的 check_conflict MOVE 我们现在同样发。
- frontend / regression / language / conformance p1,frontend / regression / difftest p3 p4,171 条 oracle 语料,
  官方 Lua 套件 p1 全绿;gofmt / vet / lint 干净。

## 教训

### 模板要让参照实现里每一个 `if` 各为真一次;进了桶的差异要跑例子判语义

这一格在 #262 的三轮独立审阅里**根本不在扫描面内**:三套模板里没有一条「局部变量作多目标赋值目标」的形状
(#262 反思 §5 ⑥ 记了这一点)。但补了「目标 = 局部变量」这一类还不够。`lparser.c` 的 `assignment` 只对逗号**之后**
的目标判 `nv.v.k == VLOCAL`(第一个目标不判),所以第一轮全范围终审补的七条局部目标模板里只有三条
(`local a ; x , a = 1 , 2`、`local a , b ; a , b = 1 , 2`、`local a , b ; a , b = f ( )`)真的进了 `check_conflict`;
而进了的三条也没有一条让 `check_conflict` **内部**的两个条件——此前某个 `VINDEXED` 目标的 `info`(表寄存器)或
`aux`(键寄存器)等于这个局部——为真,拷贝 MOVE 从未发出。漏掉的不是一个操作数类别,而是一个 helper 内部、比较
**两个目标之间关系**的条件分支;它与 §5 ⑤ 漏 `exp` 的 and/or 产生式不是同一种漏法(那是漏一个产生式)。终审者是
另行手写 `a.x, a = 1, 2` 跑出来的。

补上冲突模板之后,dump 比对**能**抓到这一格,但要靠行号而不是 opcode:修复前望舒对 `local t = {} local a = t a.x, a =
1, 2` 发 `NEWTABLE MOVE LOADK LOADK MOVE SETTABLE`,luac 发 `NEWTABLE MOVE MOVE LOADK LOADK SETTABLE`——luac 多的
那条拷贝 MOVE 被望舒「末常量 LOADK+MOVE」这条基线就有的差异**抵消**,opcode 名和条数完全一样、只差顺序;单行例子
按 opcode 比行号多重集也判 0 差异。但 luac 的拷贝 MOVE 打的是局部目标名所在行,望舒的 store MOVE 打的是语句末行,
一旦模板在 token 间隙插换行,两边 MOVE 的行号多重集就不同(在修复前的树上跑六条冲突模板:pass 1 aligned_line_diff
= 403,全部来自四条常量收尾模板;HEAD 上为 0)。所以「dump 比对对这一格是盲的」不成立,准确的说法是:**按 opcode
名和条数分类看不出它,按行号多重集在跨行形状上能抓到,但要判它是不是语义 bug 仍然只能构造可观察输入跑一遍**。

**判据**:两条。① 模板的目标是让参照实现里**每一个 `if` 各为真一次**——不止非终结符函数里按操作数类别分叉的那些,
还包括它们调用的 helper 内部的条件(`check_conflict` 的 `lh->v.k == VINDEXED && lh->v.u.s.info == v->u.s.info`
与 `aux` 那两条);一个条件若比较的是两个目标 / 两个操作数之间的关系,模板就要同时构造两者(索引目标用局部 a 作表,
后面再以 a 作目标)。自查办法:对参照实现相关函数的每个 `if` 问「哪条模板让它为真」——注意 `if` 所在的位置,
`assignment` 的 VLOCAL 判断只对逗号后的目标生效。② 与参照实现比对 dump 时,对每一类差异至少构造一个**能观察结果**
的输入跑一遍,不能只按 opcode 名甚至条数归为「基线就有」——本例条数相同、顺序不同,是语义 bug。自查办法:差异桶分类
表每一行加一列「构造了什么输入证明无语义差异」,空着的行还没判定。两条都是 [[cross-backend-semantic-fix-sweep]]
「参照实现能本地调用时用 dump 比对表批量扫」那一节(#262 反思 §5 ⑤ / 教训 3 升格而来)的补充。

## Promotion 决策

- 教训 → [[cross-backend-semantic-fix-sweep]] dump 比对一节:模板要让参照实现(含 helper 内部、跨目标关系)的每个 `if`
  各为真一次;opcode 序列差异桶逐类构造可观察输入判语义(条数相同、只差顺序也可能是语义)。
- doc-gaps「多目标赋值缺 check_conflict」条目移入已收口。
