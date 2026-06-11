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

> 提醒:**P1(crescent 解释器)已落地(M0-M14,总验收通过),P2+ 未开始**;设计文档集仍是规范源,实现现状与已知简化见 `docs/design/p1-interpreter/implementation-progress.md`。设计文档集入口与路由见 [[index]] 的「设计文档集路由」节。
