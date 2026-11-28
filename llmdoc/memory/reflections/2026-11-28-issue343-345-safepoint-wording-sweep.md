# #343 / #345：两个小修复之后，二十轮审查都在改「哪里会回收」的说法

#343（文件句柄的 `close` 不带参数时报 `got nil`）和 #345（`collectgarbage("setpause")` / `("setstepmul")` 返回设置之前的值）
的代码改动都只有几行，2026-11-27 当天合入。之后的独立审查做了二十轮，产出四十多个提交，大部分只改文档：
`docs/design/` 下 P1、P3、P4、P5 共 21 个文件，加上 `wangshu.go`、`internal/gc/collector.go` 的 godoc。改动清单见
[implementation-progress](../../../docs/design/p1-interpreter/implementation-progress.md) 的两个 2026-11-27 条目，
产品现状见 [[doc-gaps]] 里 #343、#345、#353 到 #356 的条目。

## 审查一轮轮找到的是什么

`setpause` 一接到回收器上，设计文档里所有讲「什么时候回收」的句子都成了可以按行为核对的说法。审查按代码核对，找到的
旧说法有四类，每一类都分散在多个阶段的文档里：

1. **分配本身会回收**。#297（2026-10）之后分配只记账（`AllocCharge` / `Collector.Account`），回收只发生在安全点。
   P3 内存模型、P3 IC、P4 两处出口路径、P5 07 §8.3 等地方还写着「`Arena.Alloc` 越过阈值就回收」。
2. **安全点清单是设计时的版本**。P1 05 §5.2 和 06 §7.2 还列着 Lua 函数之间的调用和返回、`VARARG` 扩栈、只在 rehash 时检查的
   `SETTABLE`。代码里检查的只有：六个会分配的 opcode（SETGLOBAL、SETTABLE、NEWTABLE、CONCAT、SETLIST、CLOSURE）结束时、
   每次 host function 返回之后（`afterHostCall`）、`coroutine.create` / `coroutine.wrap` 分配新协程之前、P3 编译循环的跳回处
   （`State.Safepoint`）。P4 的跳回处（`LoopPreempt`）只查抢占和循环预算，不回收。完整清单以 P1 06 §6.3 为准。
3. **把小的 pause 比例当成高频回收**。阈值有下限（arena 容量的 1/16），比例设成 0 也做不到每个安全点回收；做到这件事的是
   压力模式（`SetGCStressMode`）。压力模式也有边界：`collectgarbage("stop")` 之后它不回收；公开 API 返回前的
   `HostSafepoint` 只看 `SetHostTriggeredCollect` 和阈值，不看压力模式，所以压力模式碰得到的公开 API 只有 `State.Collect`
   和 `State.MaybeCollectNow`。
4. **`Options.GCPause` 等于脚本 `setpause` 设的比例**。`NewState` 不读 `GCPause`、`MaxCallDepth`、`MaxCCalls`（#356），
   深度上限是 `maxLuaCallDepth` / `maxCCallDepth` 两个常量。

## 为什么要二十轮

每一轮都只修了被点名的那几句。同一类说法在其他文件里还有，下一轮再被找到。第 18 轮的提交标题就是「collection happens
at safepoints, not at the allocation, in the remaining places that said otherwise」，第 19、20 轮又在 P1 05 / 06、P4 05 / 08 / 09 / 10 和
P3 05 里找到同一类。原因是修的时候按「审查列出的位置」改，而不是按「这一类说法」去搜。

## 可复用的做法

- 代码改变了某个检查发生的位置（这里是 #297 把回收从分配处挪到安全点）时，当轮就把这条事实的旧说法按类别搜遍
  `docs/design/` 所有阶段，搜「分配 … 触发 / 回收」「back edge … GC」「setpause 调小」这类说法，不等审查逐句指出。
  [[multi-doc-drafting]] 和 [[2026-06-11-design-review-round]] 里的「裁决后即时 grep 同步」说的是同一件事，这次是代码先变、文档后跟。
- 一条会被多处引用的清单只在一个地方写全（这里是 P1 06 §6.3），别处写一句话加指针。这一轮之前 05、06 §7.2、10、12 和
  P3 08 各自有一份清单，互相不一致。
- 「压力模式下测试通过」只说明窗口里有安全点的那些路径没有漏 pin，见
  [[prove-the-path-under-test]] §2.2d。
