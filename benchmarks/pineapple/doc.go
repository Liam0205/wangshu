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
// 对照三路(本轮跑通):
//   - Gopher       —— pineapple 用 gopher-lua backend(-tags=lua_gopher)
//   - WangshuP1    —— pineapple 用 wangshu 默认 build(新月解释器,P3 dead-code)
//   - WangshuP3Auto —— pineapple 用 wangshu p3 build + 自然热度升层
//
// 本轮**未实现**的第四路 WangshuP3Force(force-all 升层)留 follow-up:
// pineapple 的 wangshu pool 内部管理 state,公共 API 不暴露句柄,wangshu
// bench 端没法注入 SetForceAllPromote(true)。需 pineapple cross-repo 工作
// 暴露 backend hook。
//
// 真升层验证:wangshu 公共面有 State.PromotionCount() testing-only API
// (跑前=0、跑后>0 可证 Proto 升层发生)。但 pineapple pool 内 state 句柄
// 同样不暴露,本包 bench **无法白盒断言**;改用「p1 vs p3 数字差异」
// 隐式证(prove-the-path-under-test guide):p3 明显快于 p1 → 升了 + wasm
// 收益压倒采样开销;p3 ≈ p1 或更慢 → 没升或收益不够。
//
// wangshu 自身的 PromotionCount() 在 wangshu repo 根目录的
// promotion_count_p3_test.go 里有独立验证(force-all + 内层函数 → 升)。
//
// 依赖管理:pineapple 经 scripts/fetch-pineapple.sh 临时 clone 到 .pineapple/
// (.gitignore 隐藏)。本地开发者跑 bench 前需先 `make bench-pineapple-fetch`
// (顶层 wangshu Makefile 入口;或 `make fetch` 经 benchmarks/pineapple/
// 本地 Makefile;或直接调 scripts/fetch-pineapple.sh)。CI 在 workflow
// 步骤里 fetch。详见 README。
package pineapple_bench
