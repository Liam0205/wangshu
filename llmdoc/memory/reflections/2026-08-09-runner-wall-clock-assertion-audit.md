---
name: 2026-08-09-runner-wall-clock-assertion-audit
description: >
  共享 CI runner 把两个本地看似宽松的 wall-clock 断言放大到失真。稳定判据是：测试应直接断言
  离散性质；只有机器间波动小于断言余量时，wall-clock 上限才适合作为失败条件。发现一处这类
  假失败后，应审计同包、同机制的兄弟计时断言，而不是等待下一台慢 runner 逐个暴露。
metadata:
  type: reflection
  date: 2026-08-09
---

# 共享 runner 不是产品计时器（2026-08-09）

## 发生了什么

`TestBulkBuildersChargeTheStepBudget` 已经直接断言 step budget 会触发，却又要求每个用例在本机
推导出的 wall-clock 上限内结束。本地约 1.5 秒的用例在繁忙 CI runner 上用了 24.2 秒，放大约
16 倍，超过测试预留的 10 倍余量；分支没有改 Go 产品代码，失败测到的是 runner 负载。

删除这条上限后，同包审计又发现 `TestInsertShiftCostBandIsCheap` 的 5 秒上限本地耗时 3.63 秒，
只有 1.38 倍余量。按已经观察到的 16 倍机器差异投射，它同样必然会偶发失败，只是尚未碰到足够慢
的 runner。这里真正应锁住的是离散性质：shift span 处在 harness skip 阈值与产品 cap 之间；已有
`TestInsertShiftThresholdsStayDistinct` 直接断言这两个阈值分离。耗时保留为日志即可，不参与成败。

## 稳定判据

1. 先写清测试真正要证明的离散性质（预算是否触发、路径是否执行、两个阈值是否分离），优先直接
   断言它；不要再用 elapsed time 间接代理同一性质。
2. wall-clock 断言只有在**已知机器间波动小于断言余量**时才有意义。共享 runner 上的内存复制、
   调度和 `-race` 放大通常不满足这个前提。
3. 若耗时只用于提示显著退化，改为 `t.Logf` 保留可见度，不让环境噪声决定测试成败。
4. 一处计时断言被证明在测环境后，立即搜索同包、同一资源机制和相近注释中的兄弟断言。此类缺陷
   不会主动成批报错，只会等不同 runner 逐个触发。

这不否定所有 watchdog 测试。若 wall-clock 本身就是产品契约，而且最慢可达写法已经测量、跨机器
余量足够，计时仍是正确的量；关键是先确认被断言的量就是要保护的性质。
