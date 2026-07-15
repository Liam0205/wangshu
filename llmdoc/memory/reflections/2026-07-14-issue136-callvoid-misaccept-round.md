---
name: 2026-07-14-issue136-callvoid-misaccept-round
description: >
  2026-07-14 定时巡检补跑轮之一(分支 fix/nightly-136-callvoid-misaccept,PR #139,
  CI 全绿)。nightly P4 force-all fuzz 报的 issue #136(corpus 933229fef53684cc)是
  真实误编译:一个长度 6 的 proto `GETUPVAL; CALL 0 1 2; SETGLOBAL 0; LOADK 0;
  RETURN 0 2` 被 analyzeCallVoidForm 的长度 6 `Code[1]==CALL` 分支误接受成「0 参
  1 返 getter」,该分支只校验尾部隐式 RETURN,从不检查 CALL 与 RETURN 之间的指令,
  于是 SETGLOBAL 和覆盖返回槽的 LOADK 被静默丢弃,返回了 CALL 结果(make 闭包)而
  非 LOADK 的 0。破坏只在 proto 第二次进入时显现(第一次寄存器状态恰好看起来对)。
  修法:CALL 与 RETURN 之间只允许多返值 MOVE 拷贝,其余任何 op 都拒绝该形态。
  核心可复用教训:① spec-template 形态匹配器只按「首尾锚点」接受、不校验中间指令
  是致命漏洞——形态匹配必须逐条覆盖 proto 的每一个 op,任何未检查的空隙都会静默
  吞掉真实语义;② 「第二次进入才错」类误编译要求测试语料反复调用被测 proto,单次
  调用是结构盲区;③ 用 gate 开关二分定位是错误方向时,继续用「精确最小复现 + 逐
  op 消去」定位真正的形态匹配分支。
metadata:
  type: reflection
  date: 2026-07-14
---

# issue #136:call-void 形态匹配器吞掉中间指令返回过期 CALL 结果(2026-07-14,PR #139)

> 范围:分支 `fix/nightly-136-callvoid-misaccept`,PR #139(CI 全绿)。2026-07-14
> 定时巡检补跑轮处理的三个 crasher 之一(另两个:#137 前端比较左操作数常量顺序、
> #135 不可复现资源耗尽,各自独立 PR)。

## 任务

nightly P4 force-all fuzz 报 corpus `933229fef53684cc`:

```lua
local function make()x=0 return function()A=make()return 0 end end f=make()return f()%A()
```

本地重放确认是当前 master 上的真实误编译:P1 正确返回 `nan`,P4 报
`attempt to perform arithmetic on a function value`。

## 根因

被调 `A`(即 make 返回的内层闭包)的 proto 字节码是:

```
GETUPVAL 0 0        ; make (upvalue)
CALL     0 1 2      ; R0 = make()   (返回一个闭包)
SETGLOBAL 0 -1      ; A = R0
LOADK    0 -2       ; R0 = 0
RETURN   0 2        ; return R0
```

这个长度 6 的 proto 被 `analyzeCallVoidForm`(`internal/gibbous/jit/compiler.go`)
的长度 6 `Code[1]==CALL` 分支误接受。该分支是为「0 参 2 返 getter」形态
(`CALL; MOVE; MOVE; RETURN`)设计的,但它**只校验尾部隐式 RETURN**(`Code[5]`),
从不检查 CALL 与 RETURN 之间的 `Code[2]`/`Code[3]`。于是:

- `SETGLOBAL 0`(把闭包写进全局 A)被静默丢弃;
- `LOADK 0 -2`(把返回槽 R0 覆盖成 0)被静默丢弃;
- 返回了 CALL 的结果(make 闭包)而不是 LOADK 的 0。

关键细节:**破坏只在 proto 第二次进入时显现**。第一次进入时寄存器 R0 的历史状态
恰好让结果看起来对(误编译产出的 prelude 仍执行了 CALL、SETGLOBAL 副作用),只有
反复进入后过期寄存器才被观察到返回。而且被调返回值必须是**堆引用**(闭包)才可见
——返回数字的版本因为值一致而不分歧。

## 修法

CALL 与 RETURN 之间的每一条指令必须是多返值 MOVE 拷贝(call-void 的 prelude 把
所有实参加载都放在 CALL **之前**)。任何其它 op 都说明这不是 call-void 形态,拒绝
该形态让它路由到忠实路径:

```go
for k := callIdx + 1; k < retIdx; k++ {
    if bytecode.Op(proto.Code[k]) != bytecode.MOVE {
        return shapeInfo{}, false
    }
}
```

`clC>=3` 的 N 返值 getter 分支本来就会再校验这些 MOVE 的确切操作数;这道守卫额外
覆盖了 `clC==1` setter 与 `clC==2` 1 返值 getter 两个分支——它们此前从不检查这段
空隙。合法的 getter/setter/带参形态仍然升层且逐字节一致(与 lua5.1 和解释器都
比对过)。

## 验证

- 合法 call-void 形态(0/1/2 返值 getter、setter、带参 getter)仍升层 + 逐字节
  一致(differential 比对 lua5.1 与解释器);
- p4 difftest / root / jit 套全绿,default build 全绿(compiler.go 是共享代码);
- crasher corpus `933229fef53684cc` 重放通过并入库常驻回归;
- p4 difftest 语料新增 `p4_callvoid_result_overwritten_by_loadk`,被调 proto 在
  循环里调用 20 次,确保第二次进入路径真的被走到。

## 教训

### 教训 1:spec-template 形态匹配器必须逐条覆盖 proto 的每一个 op

这个 bug 的本质是形态匹配器只按「首尾锚点」接受(`Code[1]==CALL` + `Code[5]==RETURN B=1`),
把中间的 `Code[2]`/`Code[3]` 当成「一定是我期望的 MOVE」而不校验。任何未检查的指令
空隙都会静默吞掉真实语义——这里吞掉了 SETGLOBAL 副作用和覆盖返回槽的 LOADK。判据:
写「按形状接受某类 proto」的匹配器时,proto 的**每一个** op 都必须被某条校验覆盖,
不能靠「长度对上了 + 首尾对上了」推断中间。这与 [[backend-capability-vs-profitability]]
的「按 shape 拒收要写拒绝侧默认」同域,但维度不同:那条讲拒绝条件要保守,这条讲
**接受条件要穷尽**——接受一个形态等于承诺它的每条指令都被忠实翻译,任何没被 case
覆盖的 op 都是一张空头承诺。首次样本,暂留观察;P5 trace JIT 或新 spec 形态再现
同类「锚点接受、中间不查」可升 guide。

### 教训 2:「第二次进入才错」类误编译要求语料反复调用被测 proto

这个 miscompile 单次调用被测 proto 时不可见(第一次进入寄存器状态恰好对),只有
第二次及以后进入才返回过期值。P4 difftest 的既有约定正是「每个核反复调用确保升层
后 P4 分支真被走到」,本轮把 `p4_callvoid_result_overwritten_by_loadk` 的被调
proto 放进 20 次循环正是这条约定的又一次兑现。与 [[prove-the-path-under-test]]
家族「绿色 ≠ 在测你以为在测的」同源:单次调用的绿色掩盖了二次进入的 bug,反复调用
才把真正要测的路径暴露出来。memory 内反引,不新增 guide 正文。

### 教训 3:gate 二分是错误方向时,回到精确最小复现 + 逐 op 消去

第一反应是用 `SetSegToSegEnabledForTest` / `SetInlineGetUpvalEnabledForTest` /
`SetMathIntrinsicsEnabledForTest` 三个 gate 开关二分定位,但全部关掉 bug 依然在——
说明不是 seg2seg / inline-getupval / intrinsic 快路径。转而用「精确抄写出错 proto
的字节码 + 逐个变体消去」定位:先确认 AnalyzeShape/AnalyzeNative/PreferNative 都
拒绝该 proto(排除 peroptranslator 路径),再回到 compiler.go 的 spec-template
分派链,按长度 6 + `Code[1]==CALL` 锁定 `analyzeCallVoidForm`。教训:gate 二分只
在「bug 由某个可开关的快路径引入」时有效;当所有相关 gate 关掉 bug 仍在,应立刻
转向「最小复现 + 沿分派链逐分支排查」,不要在 gate 开关上反复试。首次样本暂留。

## promotion 决策

- 教训 1(形态匹配器接受条件必须穷尽每个 op)首次样本暂留观察,候选未来并入
  [[backend-capability-vs-profitability]] 作「接受侧穷尽性」对偶,或独立立项;
- 教训 2 是 [[prove-the-path-under-test]] 家族既有断言的又一实证,memory 内反引;
- 教训 3(gate 二分失败转最小复现)首次样本暂留。

## 触发场景

- 写或改「按 proto 形状接受某类快路径」的 spec-template 匹配器时(教训 1:每个 op
  都要被某条 case 覆盖,别靠长度 + 首尾锚点推断中间);
- 给「只在第二次/多次进入才错」类 tier 误编译写回归语料时(教训 2:被测 proto
  必须在语料里反复调用);
- 用 gate 开关二分定位误编译但全部关掉仍复现时(教训 3:转最小复现 + 沿分派链逐
  分支排查)。
