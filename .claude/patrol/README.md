# 定时巡检辅助脚本

每天 03:07 的巡检任务用到的两个小工具。任务本身是会话级的调度项,不在这里。

## `lock.sh` — 单实例保证

一次巡检可能超过 24 小时(遇到硬 crasher 加几轮审计),所以 03:07 再次触发时上一轮可能还在跑。

用**带截止时间的标记文件**而不是 PID 存活检查:巡检跑在助手会话里而不是某个 shell 进程里,
写锁的那个 PID 在下一次检查时早就退出了 —— 第一版用 PID 判活,结果它把自己仍然有效的锁误判成
陈旧并抢占。截止时间不需要活进程就能自愈:崩掉的那一轮,它的租约会自己到期。

```bash
.claude/patrol/lock.sh acquire      # 默认 36 小时租约;已有有效租约则 exit 1
.claude/patrol/lock.sh renew 36     # 还在干活时续租
.claude/patrol/lock.sh release      # 收工
.claude/patrol/lock.sh status
```

## `nightly-streak.sh` — nightly 连续成功次数

只数**已结束**的 run:正在跑的 run 的 conclusion 是空的,既不能算成功、也不该打断连续计数。
