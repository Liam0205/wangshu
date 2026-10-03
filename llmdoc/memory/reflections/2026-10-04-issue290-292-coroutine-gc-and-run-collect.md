---
name: 2026-10-04-issue290-292-coroutine-gc-and-run-collect
description: >
  #281-#288 一轮另开的三个 issue(#290-#292)在分支 `fix/290-292-vet-coroutine-gc-run-collect` 一起修。教训:
  **upvalue 关闭点要按 PUC 的每个 `luaF_close` 调用点逐个对照**(#285 补了 pcall 和协程错误死亡，#292 又漏了顶层
  `callOnStack`,三处同一类);**把 Go 侧状态改成随 arena 对象存亡时，先找 PUC 的对应做法，而不是自己设计迭代算法**
  (开放 upvalue 直接标记所指栈槽，照 `reallymarkobject`,比「先标记、再对未达线程反复补标」简单且等价);
  **GC 压力模式只配小脚本**(分配百万次的脚本开压力模式要跑 48 秒)。
metadata:
  type: reflection
  date: 2026-10-04
---

# #290-#292:P4 vet 警告、协程回收、出错 Run 后的 Collect(2026-10-04)

> 范围：分支 `fix/290-292-vet-coroutine-gc-run-collect`。对账表在 P1 implementation-progress 的
> 「#281-#288 一轮另开的三个 issue」条目，这里只记过程和教训。

## 1. upvalue 关闭点按 PUC 的调用点逐个对照(#292)

#292 和 #285 暴露的问题是同一类：出错展开的帧没有执行 RETURN,upvalue 一直开着。#285 修了 pcall
(`callLuaFromHostNamed`)和协程错误死亡(`Resume`),但顶层 `callOnStack` 仍把关闭推迟到下一次 Run,
两次 Run 之间宿主调 `Collect()` 就会回收闭包引用的值。

5.1.5 里 `luaF_close` 的调用点一共几处:`luaD_pcall` 出错时(`ldo.c`)、无保护抛错的 `resetstack`、`OP_RETURN` /
`OP_TAILCALL` / `OP_CLOSE`(`lvm.c`)、`luaE_freethread` 与 `lua_close`(`lstate.c`)。`lua_resume` 的错误路径不关，
望舒在协程错误死亡时关，是因为死协程的栈不再是根(#285)。当时如果把这几处和望舒的每个出错返回点逐一对照，顶层
`callOnStack` 对应 `luaD_pcall`,在 #285 那轮就能发现。以后改 upvalue 关闭或错误展开，先列这张对照表。

## 2. 协程回收：照 PUC 的标记方式，而不是自己设计算法(#291)

协程原先是 lightuserdata 句柄，所有非 dead 协程都是根。改成 arena 里的 Thread 对象后，难点在于：没有引用的
协程的栈不会被扫，可它栈上的开放 upvalue 可能还被一个活着的闭包共享。

第一版写成「标记结束后，对没被标记的线程反复补标它们的开放 upvalue 值，直到不再有新标记」,写到一半删掉了。
`lgc.c` 的做法是 `reallymarkobject` 标记开放 upvalue 时直接标记它所指的栈槽值，一次标记就完成，没有迭代。照这个做，
gc 包只多一个 `Roots.OpenUpvalue` 钩子，回收前再 `closeUpvals` 保住闭包看到的值。

另一个要注意的地方是 `coroutine.wrap`:返回的宿主闭包在 Go 闭包里捕获了协程，收集器看不到。`auxwrap` 是把协程放在
C 闭包的 upvalue 里，这里也一样，用 `MakeHostClosureKeeping` 把协程放进宿主闭包的 upvalue。

## 3. GC 压力模式只配小脚本

#291 的回归测试最初把 issue 原文的复现(50 个协程各建 20000 元素的表)也放进压力模式循环，单个测试跑 49 秒。
压力模式在每个分配点做一次完整收集，耗时与「分配次数 × 存活堆大小」成正比。修法是把测试拆成两个：内存测量的
那部分只跑普通模式，验证存活关系的小脚本才跑压力模式，两个合计 0.3 秒。
