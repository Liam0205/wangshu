//go:build !wangshu_profile

package crescent

// profileEnabled 是 P2 计数开关的编译期常量(`docs/design/p2-bridge/01-profiling.md`
// §3.5 路线 B 旁路计数 + §0.1 不变式 1):
//
//   - 默认 false:回边 / 入口采样钩点全段被 Go 编译器消去——P1-only 部署
//     完全不付计数税(零开销),与 P1 行为逐字节一致。
//   - `go build -tags wangshu_profile` 启用为 true,激活 P2 决策机。
//
// 这是「每阶段独立交付」(原则 3)在 P1 ↔ P2 边界的物理兑现:**P2 启用是
// 内部行为切换,公共 API 不变**。
const profileEnabled = false
