// Package bridge is the P2 layered-bridge subsystem (basecraft, not an
// execution layer).
//
// 设计文档:docs/design/p2-bridge/(00-overview..06-testing-strategy)。
//
// P2 一句话定位:在 P1 解释器(crescent)之上加一台「分层决策机器」,产出
// 三样东西(热度、IC 类型 feedback、可编译性判定)喂给 P3/P4 编译层,自己
// 不在执行热路径上(`docs/design/p2-bridge/00-overview.md` §1)。
//
// 包布局(每个文件一组紧密耦合的类型,不与执行层 internal/crescent 互相
// 依赖——bridge 是基建非执行层):
//
//   - profile.go        热度计数(回边 / 入口)与 ProfileData 字段
//   - feedback.go       TypeFeedback / PointFeedback / FeedbackKind 枚举
//   - compilability.go  Compilability 三态枚举与 reasonsBitmap
//   - tier.go           TierState 状态机三态枚举
//   - p3compiler.go     P3Compiler 接口(P3/P4 共享前端)与 GibbousCode 抽象
//   - bridge.go         Bridge 主结构 + onBackEdge / onEnter 钩点 +
//     considerPromotion 状态机入口
//
// **重要不变式**(贯穿全包,失守即设计失败):
//
//   - bridge 不依赖 internal/crescent —— 反向钩点由 crescent 端注入
//     (interface + setter)避免循环依赖。
//   - bridge 自己不发射代码、不跑 Proto;一旦本包出现「跑 Proto」逻辑
//     就违反了 P2 「自己不在执行热路径」的铁律。
//   - 所有 P2 计数自增、可编译性查询都要在 ProfileEnabled() 为 false 时
//     可被编译期消去(P1-only 部署零开销)。
//
// 状态机不变式:TierState 单向 + 吸收(无 TierGibbous→TierInterp 反向边),
// 这是 P2/P3 「零 deopt」的形式化体现(`04-try-compile-fallback.md` §2.4)。
package bridge
