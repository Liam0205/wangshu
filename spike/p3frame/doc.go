// Package p3frame 是 PW10「零跨界」里程碑 Stage 0 spike(独立 go module,
// 不污染主库零依赖)——验证生死假设:gibbous→gibbous 调用的帧建拆搬进 Wasm
// (段字写 + ciDepth 字增减 + maxOpenIdx 守卫),是否真比当前经 h_call + h_return
// 两次 host 跨界更快。
//
// 决策报告见 DECISION.md(永久存档)。结论 🟢 GREEN:in-Wasm ~8.5ns/call vs
// twocross ~90ns/call(快 10.5x),足以把 call 核拉到 ≥1x ⟹ 放行 Stage 1+ 重写。
//
// 承 PW0 spike/p3boundary + PW10-Phase0 spike/p3indirect 先例(里程碑级架构改动
// 配 spike 闸门,绝不盲启多会话重写)。
package p3frame
