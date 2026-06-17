// Package pineapple_bench:wangshu 作为 pineapple 默认 lua backend 时的真实
// 使用形态 benchmark。
//
// 设计动机:wangshu 自己的 baseline / realworld / embedded 三档 benchmark 都
// 是「我们想象的 boundary-dominated 形态」,跟下游真实用法可能有偏。pineapple
// (https://github.com/Liam0205/pineapple)是 wangshu 第一个生产消费者,它的
// transform_by_lua operator 经 pine.Engine.Execute 调起 wangshu;本包用 pineapple
// 公共 API + 最简 pipeline 配置直接跑 LuaOp,反映 wangshu 在真实下游用法下的
// 性能数字。
//
// 对照四路:
//   - Gopher       —— pineapple 用 gopher-lua backend(-tags=lua_gopher)
//   - WangshuP1    —— pineapple 用 wangshu 默认 build(新月解释器,P3 dead-code)
//   - WangshuP3Force —— pineapple 用 wangshu p3 build + SetForceAllPromote(true)
//   - WangshuP3Auto  —— pineapple 用 wangshu p3 build + 自然热度升层
//
// 真升层探针:wangshu v0.x rcN 起公共面有 State.PromotionCount() testing-only
// API。P3Auto 路径加白盒断言:跑前=0、跑后>0 才算真升层。否则数字不可读
// (prove-the-path-under-test guide)。
//
// 依赖管理:pineapple 经 scripts/fetch-pineapple.sh 临时 clone 到 .pineapple/
// (.gitignore 隐藏)。本地开发者跑 bench 前需先 `make fetch`(或直接调
// scripts/fetch-pineapple.sh)。CI 在 workflow 步骤里 fetch。详见 README。
package pineapple_bench
