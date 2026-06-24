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

> 提醒:**P1(crescent 解释器)完整交付 + P2(bridge 分层桥)PB0-PB7 + 后续优化轮 #1-#4 + P3(gibbous/wasm 编译层)PW0-PW10 全卷已收口**(M0-M14 + P1 收尾轮 + P2 单会话冲刺 2026-06-13 + P3 翻译器到端到端总验收冲刺 + PW10 跨层调用税消除轮)。**P2 后续优化轮**(stdlib 白名单 + 阈值校准占位 + sync.Pool (C) + megamorphic;设计文档原称 `P2+`,**不是 P3**)已融入 P2 主交付。**P3 全卷已交付**:PW0 spike 闸门(wazero call boundary 36.7ns<150ns)→ 包骨架 + arena 收养 wazero linear memory → 全 38 opcode 除 VARARG 外翻译成 Wasm(直线/算术/比较/控制流 relooper/表 IC inline 跳哈希/CALL·TAILCALL 跨层互调/CLOSURE·CLOSE/数值·泛型 for)→ 线程级 tier 规则(协程不升层)→ gcPending inline 回边零跨层 → V1-V18 端到端总验收(正确性轴层间逐字节 + 四 build + `-race`,性能轴 loop 核 2.58x over 解释器)。**PW10 消除 gibbous→gibbous 跨层调用税:R1/R2/R3/R3.5 + 零跨界 ①/基建-a/基建-b/③a/③b/④-i 骨架/顶层升层 callOnStack 全过线**(Phase 0 spike `spike/p3indirect/` S-A/S-B/S-C 裁定 Arch-2 共享 imported funcref 表而非 rebuild-all + R1 共享 funcref 表基建 + R2 CallInfo→linear-memory 迁移 = 长期延后的 **VS0-e**,4 word/帧 + R3 `call_indirect` 直接分派消 `code.Run` 重入 + R3c-fix 出错点就地标注 + R3.5 host helper `WithFunc`→`WithGoFunction` 消反射装箱 + 零跨界 ① top mirror 字基建 + 基建-a closure slot 缓存 + 基建-b proto cache 段 + ③a savedTop 基建 + ③b emitReturn 守卫快路径 + ④-i emitCall 守卫骨架 + fastCallHits mirror 字 + callOnStack 顶层升层 cl 直接走 enterGibbous + TopLevelUplift 探针 + emit 原语 i64.add/i64.or 保留供未来 ④-ii);**本机 Xeon 6982P 2s×3 count 实测基线(2026-06-16)**:**loop 2.95x(+10% over R3.5 2.67x,③ RETURN 拆帧真实收益)/ table 0.88x / call 0.52x / mixed 0.99x**;**call 0.52x 是 bench kernel 结构性架构边界**——profile `/tmp/call.prof` 实证四 kernel body 含 ReasonUnknownCall(F2-b 静态分析不能确定被调函数不 yield)→ body 不可升 → 顶层升层 + ④ emitCall fast body 均对 bench kernel 无显著效果;**④-ii fast body 未交付**(预估上限 0.57x 仍 <1x,实现复杂度 ~200 行 wasm 字节级 codegen,UAF 高,ROI/UAF 不利留 followup,emit 原语已保留);**PW10 收口为「已落地子里程碑 + 架构边界文档化」**,旧文档所述「四核全翻面」+「剩 R4/R5 待实现」失实须替换;**VS0-e 全量收口(2026-06-16)**——varargs 进栈下区子步 ①~④(`c22798b`/`4e50687`/`966318c`/`ed95020` + 反思 `b9a2c54`),官方 Lua 5.1 真栈布局 `[func | vararg | R(0)..]` 落地,退役 `ci.varargs` Go slice + `th.ciVarargs` 影子;**P4 设计文档集已扩展(2026-06-24)**——单文件 360 行 → 子目录 10 文件 ~8150 行(00-08 + implementation-progress,与 P2/P3 同档详细深度),含 PJ0-PJ10 里程碑预设 + V1-V22 验收口径 + 37 项跨文档回填请求登记,设计稿状态由「架构决策深度」升级为「详细设计深度」;P5 仍是单文件架构决策深度。设计文档集仍是规范源,P1 实现现状与 P3 迁移留口见 `docs/design/p1-interpreter/implementation-progress.md` 对账表;P2 实施现状见 `docs/design/p2-bridge/implementation-progress.md`(PB0-PB7 + #1-#4 全部 ✅);P3 实施现状(PW0-PW9 ✅ + §11 PW9 验收对账 + §12 PW10 R1-R2 + §13 R3+R3.5 + §14 零跨界 RETURN 拆帧 + §14.8 ④-i + §14.9 顶层升层 + §14.10 ④-ii 架构边界 + 回填请求收口表 RW-1~11)见 `docs/design/p3-wasm-tier/implementation-progress.md`;**P4 实施现状(立项前)见 `docs/design/p4-method-jit/implementation-progress.md`**(PJ0-PJ10 待启动 + 37 项 RJ 回填请求)。设计文档集入口与路由见 [[index]] 的「设计文档集路由」节。
