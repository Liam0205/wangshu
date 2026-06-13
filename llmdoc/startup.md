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

> 提醒:**P1(crescent 解释器)完整交付 + P2(bridge 分层桥)PB0-PB7 + 后续优化轮 #1-#4 全过线;P3(gibbous/wasm)详细设计已齐备(子目录 9 文件,2026-06-13 扩展轮),实现未启动**(M0-M14 + P1 收尾轮 + P2 PB0-PB7 + 后续优化轮 #1-#4 单会话冲刺,2026-06-13)。**P2 后续优化轮**(stdlib 白名单 + 阈值校准占位 + sync.Pool (C) + megamorphic;设计文档原称 `P2+`,**不是 P3**)本轮全部融入 P2 主交付。**P3 实现未启动——PW0 开工前置 spike(wazero call boundary <150ns)是生死闸门**,先于一切翻译工作;P4/P5 仍在规划。设计文档集仍是规范源,P1 实现现状与 P3 迁移留口见 `docs/design/p1-interpreter/implementation-progress.md` 对账表;P2 实施现状见 `docs/design/p2-bridge/implementation-progress.md`(PB0-PB7 + #1-#4 全部 ✅);P3 实施现状 + 回填请求收口表见 `docs/design/p3-wasm-tier/implementation-progress.md`(全 PW ⏳,RW-1~11 待 P3 落地)。设计文档集入口与路由见 [[index]] 的「设计文档集路由」节。
