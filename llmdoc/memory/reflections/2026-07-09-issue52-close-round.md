---
name: 2026-07-09-issue52-close-round
description: issue #52 收口轮(PR #99):四 op 双架构 exit-reason 接入 + P4 验收测试抓到共享三层的 doTailCall multret 既有 bug
---

# issue #52 收口轮(2026-07-09,PR #99,分支 `feat/issue52-remaining-ops`)

## 一句话

补齐 `opSupported` 最后四个 op(TAILCALL / TFORLOOP / CLOSURE / CLOSE,双架构 exit-reason),关闭 issue #52;过程中 P4 验收 e2e 抓到一个 clean master 上就存在、P1/P3/P4 三层共享的 `doTailCall` 丢多返回值 bug。

## 过程要点

- 四个 op 的协议形态各镜像一个既有先例(TAILCALL→HelperReturn 终止 run / TFORLOOP→HelperCompareSlow verdict 回段 / CLOSURE→P3 伪指令 skip / CLOSE→普通往返),没有发明新协议——**先例复用使 review 三轮全 APPROVE 零阻塞**。
- CLOSURE 伪指令(数据非 op)要求所有翻译器 pc 走查统一步进:与其在四处走查各自加 skip,抽一个共享 `nextRealPC` 让漏改成为编译期显式行为(走查处样板一致,grep `pc++` 可审计)。
- **e2e 期望值先跑 oracle 再写**:TestPJ10_Closure_Close_Promotes 的手算期望(12)错了,oracle 说 27——凡断言具体输出的用例,期望值一律先经 `~/bin/lua5.1` 验证,不信手算。

## 关键教训:验收测试失败先 oracle + clean master 双向定位

多返回值 host 尾调用 e2e 失败(`return up(t)` 只回 2 个值)。定位次序:

1. **oracle**(PUC 5.1)说应回 3 个 → 期望值没错;
2. **clean master(git stash)+ 纯 P1 解释器**同样只回 2 个 → **不是本轮改动引入,是存量 bug**;
3. 根因:`doTailCall` 给 `callHost` 传父帧定长 `ci.NResults()`,PUC 是 `luaD_call(L, ra, LUA_MULTRET)`;定长分支重置 top,尾随 `RETURN B=0` 按 top 收就截断。

这是 [[prove-the-path-under-test]] §5「维度重置」家族的又一确认(不新增分支):**给此前无 native 覆盖的 op 写验收测试 = 新一格探索维度**,它抓到的往往不是新代码的 bug 而是被解释器宽容路径掩盖多年的存量 bug(先例:PR #83 FuzzAutoPromote 上线数天抓到 FORPREP 缺守卫)。操作纪律:新路径测试失败时,**先 oracle 定期望、再 clean master 定归属**,两步都做完才决定是「修我的改动」还是「独立 commit 修存量 bug + conformance 锁三层」。

## CI 基础设施噪声

本轮 CI 三次失败全部是 GHA hosted runner 池未接单("The job was not acquired by Runner of type hosted even after multiple attempts",job 显示 cancelled、跑 18m+),不是测试失败。识别特征:job conclusion=cancelled 且 steps 为空 + check-run annotation 是上述文案;处置就是 `gh run rerun <id> --failed`,不需要动代码。首次样本,暂不成篇。
