# 启动阅读顺序

> 全局类别目录与文档路由在 [[index]],本文件不重复;这里只给 MUST 启动顺序与升级提示。

## 每次任务的启动阅读顺序

1. **[[index]]** — 全局文档地图,确定你的问题该路由到哪篇。
2. **本文件(startup)** — 确认 MUST 启动顺序。
3. **MUST 文档**:
   - **[[design-premises]]** — 唯一的 MUST。四组不可妥协前提(列内核负载形状、Go runtime 四项税、五条贯穿原则、第一天值表示承诺)。任何技术判断前先过这一篇。

## 读完 MUST 之后该读什么(升级提示)

- 要理解**项目是什么、不做什么** → [[project-overview]]。
- 要理解**演进路线 / 阶段门槛 / 月相 tier** → [[evolution-roadmap]]。
- 要理解**值表示 / 内存 / GC 决策** → [[value-representation]]。
- 要理解**宿主如何嵌入、API 与 arena ABI** → [[embedding-contract]]。
- 遇到不认识的术语或想查参照项目 → [[glossary]]。

> 提醒:**P1(crescent 解释器)完整交付 + P2(bridge 分层桥)PB0-PB7 + 后续优化轮 #1-#4 + P3(gibbous/wasm 编译层)PW0-PW9 + PW4b 全过线**(M0-M14 + P1 收尾轮 + P2 单会话冲刺 2026-06-13 + P3 翻译器到端到端总验收冲刺)。**P2 后续优化轮**(stdlib 白名单 + 阈值校准占位 + sync.Pool (C) + megamorphic;设计文档原称 `P2+`,**不是 P3**)已融入 P2 主交付。**P3 全卷 PW0-PW9 已交付**:PW0 spike 闸门(wazero call boundary 36.7ns<150ns)→ 包骨架 + arena 收养 wazero linear memory → 全 38 opcode 除 VARARG 外翻译成 Wasm(直线/算术/比较/控制流 relooper/表 IC inline 跳哈希/CALL·TAILCALL 跨层互调/CLOSURE·CLOSE/数值·泛型 for)→ 线程级 tier 规则(协程不升层)→ gcPending inline 回边零跨层 → V1-V18 端到端总验收(正确性轴层间逐字节 + 四 build + `-race`,性能轴 loop 核 2.58x over 解释器)。**PW10 消除 gibbous→gibbous 跨层调用税进行中**:Phase 0 spike(`spike/p3indirect/` S-A/S-B/S-C,裁定 Arch-2 共享 imported funcref 表而非 rebuild-all)+ R1(共享 funcref 表基建,各 module 自注册 `run`)+ R2(CallInfo→linear-memory 迁移 = 长期延后的 **VS0-e**,4 word/帧)已交付;**剩 R3(`call_indirect` 直接分派——真正付清性能的步骤)+ R4 + R5(re-bench)待实现**(R3 未做前 V15 geomean 仍未过、call 核退化源于 `h_call` 双跨层 ~143ns 边界税,正是 R3 要修的);P4/P5 仍在规划。设计文档集仍是规范源,P1 实现现状与 P3 迁移留口见 `docs/design/p1-interpreter/implementation-progress.md` 对账表;P2 实施现状见 `docs/design/p2-bridge/implementation-progress.md`(PB0-PB7 + #1-#4 全部 ✅);P3 实施现状(PW0-PW9 ✅ + §11 PW9 验收对账 + §12 PW10 R1-R2 对账)+ 回填请求收口表(RW-1~11)见 `docs/design/p3-wasm-tier/implementation-progress.md`。设计文档集入口与路由见 [[index]] 的「设计文档集路由」节。
