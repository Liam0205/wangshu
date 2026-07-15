---
name: 2026-07-14-issue137-cmp-const-order-round
description: >
  2026-07-14 定时巡检补跑轮之一(分支 fix/nightly-137-135-frontend-const-order,
  PR #138,CI 全绿)。nightly oracle fuzz 报的 issue #137(corpus 9e5e75e5c04112a2:
  `print(0*-0~=0%0,0)`)是真实分歧:尾部字面 0 被打印成 "0",PUC 5.1.5 打印 "-0"。
  根因是常量表登记顺序——PUC luaK_infix 在解析右子树之前就把比较运算符的左操作数
  exp2RK,使折叠出的 -0 先占共享零槽(±0 去重按数值相等、先到先得),后续字面 0
  复用它 → 打印 "-0"。wangshu 的 exprCompare 把 numeral 左操作数延迟到右子之后,
  让 0%0 的 +0 抢先登记。修法:exprCompare 无条件在解析右子树前物化左操作数,镜像
  luaK_infix 的 default 分支(与 arith 分支的 isnumeral 延迟非对称是 PUC 有意为之)。
  同轮附带处理 #135(p3 auto,不可复现资源耗尽,corpus 入库常驻回归)。核心可复用
  教训:①「先物化谁」这类求值顺序在有副作用去重(常量表 ±0 先到先得)下会改变
  可观察结果,不是纯内部实现细节;② 前端 codegen 与 PUC 的顺序类分歧,权威是
  lcode.c 的 luaK_infix/luaK_posfix 分支结构,arith 与 comparison 的非对称必须逐条
  照抄不能想当然统一。
metadata:
  type: reflection
  date: 2026-07-14
---

# issue #137:比较运算符左操作数的常量登记顺序(2026-07-14,PR #138)

> 范围:分支 `fix/nightly-137-135-frontend-const-order`,PR #138(CI 全绿)。
> 2026-07-14 定时巡检补跑轮处理的三个 crasher 之一(另两个:#136 P4 call-void
> 误接受、#133 argerror caller-site,各自独立 PR)。

## 任务

nightly oracle fuzz(`FuzzOracleDiff`)报 corpus `9e5e75e5c04112a2`:
`print(0*-0~=0%0,0)`。本地重放确认真实分歧:wangshu 打印 `true	0`,PUC 5.1.5
打印 `true	-0`——尾部那个字面 `0` 被打成了 `-0` 的分歧(第二个参数)。

## 根因

这是**常量表登记顺序**问题。用 lua5.1 确认了它的顺序敏感:

```
print(0, 0*-0)   →  0    0
print(0*-0, 0)   →  -0   -0
```

PUC 的 `±0` 去重在 `addk` 里按数值相等(`0.0 == -0.0`),先到先得、物理保留先登记
那个零的符号。`luac -l` 显示 PUC 对 `print(0*-0~=0%0,0)` 只有一个数字常量 `-0`,
`MOD`、`EQ`、最后的 `LOADK` 全部复用它——因为 `0*-0` 折叠出的 `-0` 先登记进常量表。

PUC `luaK_infix`(lcode.c)对**比较运算符**走 `default` 分支,**无条件** `luaK_exp2RK(fs, v)`
物化左操作数,且这发生在解析右子树**之前**;而 arith 分支有 `if (!isnumeral(v))`
豁免(因为 arith 要留给 `constfolding` 折叠,故延迟)。wangshu 的 `exprCompare`
错误地对 numeral 左操作数也延迟到右子之后才 `exp2RK`,于是 `0%0` 的 `+0` 字面
抢先登记进常量表占了共享零槽,后续字面 `0` 复用 `+0` → 打印 `0`。

## 修法

`exprCompare` 无条件在解析右子树之前 `exp2RK` 左操作数,镜像 `luaK_infix` 的
`default` 分支:

```go
l := fs.expr(e.L)
rb := fs.exp2RK(e.Line, &l)   // materialize LEFT before parsing right (luaK_infix default)
r := fs.expr(e.R)
rc := fs.exp2RK(e.Line, &r)
```

释放顺序(先 rc 后 rb)、`>`/`>=` 的操作数交换(swap)、非 numeral 操作数路径都不
受影响(非 numeral 本来就急切物化)。`isNumeral` 仍被 arith 分支与 `exprConcat`
使用,无死代码。arith 与 comparison 的非对称是 PUC 有意为之(arith 需 constFold
故延迟、comparison 不折叠故不延迟),代码注释显式记录。

## 验证

- 副作用求值序 / swap / 字符串 / 混合比较逐字节等于 lua5.1;
- 全 default 套 + `internal/...` 全绿,cgo oracle 全绿;
- difftest 新增 `cmp_left_negzero_registers_first` 精确复现该场景;
- #137 corpus 重放通过;
- 同轮 #135(p3 auto,`77a7d64e35ed414b`)本地重放干净(大 `sum(555555520)` 循环
  = 资源耗尽型,headSha == HEAD),按 [[unreproducible-crasher-triage]] 判据 corpus
  入库常驻回归,不硬修。

## 教训

### 教训 1:「先物化谁」在有副作用去重下会改变可观察结果

直觉上「左操作数先物化还是右操作数先物化」是纯内部实现细节、不影响语义。但常量表
`±0` 去重是**先到先得且保留符号**的有副作用操作,于是「哪个字面先登记进常量表」
直接决定了共享零槽保留 `+0` 还是 `-0`,进而改变 `tostring` 的可观察输出。判据:
当一个「顺序无关」的直觉遇到「先到先得 / 首次写入保留 / 唯一化去重」这类带副作用
的共享结构时,顺序就不再无关——必须逐字节对齐参照实现的确切顺序。这是本仓
`neg_zero_fold_after_poszero` / `neg_zero_first_poszero_reuses`(issue #7 前一轮
负零常量表去重)的同族第 2 实例,已在 difftest 有专门 section。首次以「顺序敏感」
维度出现,暂留观察;第 3 实例可考虑并入前端 codegen 类 guide。

### 教训 2:前端 codegen 顺序类分歧,权威是 lcode.c 的分支结构

与 PUC 的顺序类分歧不能靠「两个操作数对称、随便先算哪个」推断,权威是 `lcode.c`
的 `luaK_infix` / `luaK_posfix` 分支结构。关键是 arith 与 comparison 的**非对称**:
arith 分支为了留给 `constfolding` 而对 numeral 延迟 `exp2RK`,comparison 分支不折叠
故无条件急切 `exp2RK`。想当然把两者统一(wangshu 之前对 comparison 也套用了 arith
的 numeral 延迟)正是这个 bug 的来源。这与 [[cross-backend-semantic-fix-sweep]]
的「PUC 语义由 C 实现定义,分歧先 grep `_lua515/` 源码」同域,本轮是该原则在**前端
codegen 求值顺序**维度的应用(以往实例多在 stdlib / 值语义)。memory 内反引,不新增
guide 正文(guide 已有该原则的总述)。

## promotion 决策

- 教训 1(顺序敏感 × 有副作用去重)首次以此维度出现,暂留观察;
- 教训 2 是 [[cross-backend-semantic-fix-sweep]]「PUC 语义源码定义」原则的又一
  维度实证,memory 内反引不新增正文。

## 触发场景

- 改前端 codegen 里「两个操作数先算哪个」的顺序时(教训 1:先问下游有没有先到先得
  的去重 / 唯一化结构,有就是顺序敏感、必须对齐 PUC);
- 与 PUC 的分歧表现为「顺序 / 先后」而非「某个值算错」时(教训 2:读 lcode.c 的
  luaK_infix/luaK_posfix 分支,注意 arith 与 comparison 的 isnumeral 非对称)。
