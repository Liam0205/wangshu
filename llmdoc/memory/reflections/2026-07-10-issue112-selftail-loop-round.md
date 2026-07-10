---
name: 2026-07-10-issue112-selftail-loop-round
description: >
  issue #112 修复轮(2026-07-10,PR #113):P4 升层后 HeavyRecursion 比 P1 解释器慢 12%——表格里唯一
  「升层反而净亏」的负载。根因不是笼统的「帧管理税」而是精确的协议特性:HelperTailCall(#52)每层递归都
  终止 native run(段退出 + host.TailCall + executeFrom + 每层 Go 重入),而 collatz 的全部递归调用都是
  TAILCALL。修法(#112 方向 1):mono 自尾调用编成段内循环——身份 guard(R(A) 全 64 位 == TagFunction
  boxing 的 currentClosureRef)→ 参数搬移 → nil-fill → loopFuel dec+jnz → jmp BB 0;guard miss 走原
  HelperTailCall 不变。HeavyRecursion 6.48ms → 1.85ms(0.88× → 3.1× vs P1)。核心教训:PUC 语义的
  「帧复用」使自尾调用与「重进本段」位级等价,这是把跨界成本塌缩成寄存器 shuffle 的语义依据;closeUpvals
  的可跳过性要靠「proto 无 CLOSURE op ⟹ 本帧局部不可能被捕获 ⟹ closeUpvals 恒 no-op」这样的编译期蕴含
  链证明,不能靠「测试没挂」;#102/#107 两轮的教训(新回边必挂 loopFuel / 身份比较必须全位宽防别名)在
  设计期直接复用,零返工。
metadata:
  type: reflection
  date: 2026-07-10
---

# issue #112 自尾调用段内循环轮反思(2026-07-10,PR #113)

> 范围:分支 `feat/issue112-selftail-loop`。P4 native 对 mono 自尾调用发射段内循环(双架构),
> 消除每层递归一次的段退出 + Go 重入往返。

## 任务

HeavyRecursion(collatz 自递归)升层后 6.48ms,比 P1 解释器的 5.81ms 慢 12%,是 README 表里唯一
「升层净亏」的负载(arm64 差距 -46%)。按 issue #112 方向 1 实现:自尾调用在段内循环。

## 期望与实际

- 期望:diagnosis 阶段(开 issue 前)已经把根因定位到 TAILCALL 协议(`luac -l` 确认 collatz 全部递归
  调用是 TAILCALL + HelperTailCall 每层终止 run),实现按图施工。
- 实际:基本按图施工,一次通过。两个设计决策在写 emit 前就被前两轮的教训锁定,零返工:
  1. **身份 guard 全 64 位比较**——第一稿想用 payload-only(低 48 位)比较,写注释时意识到这正是
     #107 的别名家族:一个普通 double 的低 48 位可以与任意 GCRef 碰撞,`return x(...)`(x 是数字)
     必须 miss 到慢路径报 "attempt to call a number value",不能进循环。改为
     `R(A) == TagFunction<<48 | currentClosureRef` 全位宽比较。
  2. **新回边必挂 loopFuel**——`function f() return f() end` 是 O(1) 深度的密封循环,没有栈溢出兜底;
     #102 的验收条款(「新的段内回边需要 loopFuel 守卫」)在 issue #112 里就写好了,emit 时直接带上。
     实测预算下 55ms 报错。

## 根因与修法(记录以备后查)

- **根因**:PUC 5.1 尾调用复用 caller 帧(doTailCall:搬 callee+args 到 funcIdx、pop、re-enter)。
  当 callee 就是正在运行的 closure 时,「复用后的帧」与「用搬移后的参数重进本段」**位级等价**——
  这是把整个跨界往返塌缩成「参数 shuffle + jmp BB 0」的语义依据。SelfTailCallHitCount 白盒探针证明
  fast path 真的执行(collatz 200 次外层调用产生 119084 次段内循环命中,零次 HelperTailCall)。
- **nil-fill 是正确性义务不是优化**:enterLuaFrame 每次清 [numFixed, MaxStack) 为 nil;段内循环不清
  的话上一迭代的局部变量会泄漏进下一迭代(difftest 用 `p4_selftail_stale_local_clear` 钉住)。同时
  它顺便覆盖了参数不足(`f()` 缺参数补 nil)的情况——与 enterLuaFrame 同一段语义,一段代码两个义务。

## 核心教训

### 教训 1(closeUpvals 的可跳过性要靠编译期蕴含链证明)

doTailCall 在帧复用前跑 closeUpvals;创建闭包捕获本帧局部的 body,每次尾调用都需要关闭当轮捕获——
段内循环跳过它的话所有迭代共享一个 open upvalue(每个捕获都看到最后一轮的值)。修法不是运行期检查,
而是编译期蕴含链:**proto 无 CLOSURE op ⟹ 不可能存在捕获本帧局部的 upvalue ⟹ closeUpvals 恒 no-op
⟹ 跳过 sound**。`TestI112_ClosureGateDeclines` 用「三个闭包各捕获自己迭代的值」钉住门的存在(探针
为 0 + 结果 byte-equal 经 fallback)。这个手法值得记住:绕过 host 隐式维护的不变式时,证明义务是
「该不变式在此形状下恒空」,而证明手段最好是字节码层面的静态蕴含,不是「跑了没挂」。
(与 [[prove-the-path-under-test]] 的反向应用:不是证明路径被执行,而是证明被跳过的语义恒空。)

### 教训 2(前轮教训的设计期复用是零返工的来源)

本轮两个最容易出 bug 的决策(guard 位宽 / 回边计费)都在写代码前被 #107 和 #102 的教训直接锁定。
值得注意的是它们都不是从 guide 里查到的,而是同一 session 内的短期记忆——**教训进 guide 的价值在
跨 session**,这正是 [[cross-backend-semantic-fix-sweep]] 与 loopFuel 三问表(#102 反思教训 3)
存在的理由。本轮相当于这两条 guide 的首次「设计期消费」实证。

### 教训 3(diagnosis 先行使实现轮几乎无探索成本)

开 issue 前的那轮分析(bench 复测 → `luac -l` 看字节码 → 定位 HelperTailCall 协议)把根因精确到
「每层一次段退出」,issue 里直接写出了修法草图 + 验收条款 + 风险清单(loopFuel/错误路径/回归面)。
实现轮从 branch 到 PR 全程 ~1.5h 无一次方向调整。对比:如果 issue 只写「HeavyRecursion 慢,查一下」,
实现轮就要自己重做全部 diagnosis。**开性能 issue 时把 diagnosis 做到「修法可以按图施工」的深度,
是把探索成本从实现轮(高压,有 CI/review 等着)搬到 diagnosis 轮(低压,可随时放下)的杠杆。**

## 过程记录

- 复测数字(amd64,-benchtime=2s -count=3 -cpu=1,同机):HeavyRecursion force 6.48→1.85ms,
  auto 6.48→1.87ms(P1 5.81ms;0.88×→3.1× vs P1,1.38×→4.8× vs gopher);fib 1.08ms /
  binary-trees 27.9ms 无回归。README amd64 行单项更新 + 重测日期脚注;arm64 行待那台机器重测。
- 接受面动了 → 90s p4 fuzz smoke,干净。
- arm64 镜像交叉编译过(linux/darwin),运行时验证交 PR CI arm64 矩阵。
