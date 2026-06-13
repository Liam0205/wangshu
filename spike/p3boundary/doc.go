// Package p3boundary is the PW0 spike harness for the P3 Wasm tier
// (docs/design/p3-wasm-tier/01-spike-gate.md).
//
// 目的:实测 wazero call boundary < 150ns 闸门(P3 生死闸门)。
// 三档样本 S1/S2/S3 + memory 共见 + arena 收养可行性 + 四项税最小验证。
//
// **独立 go module**:不污染主库 `github.com/Liam0205/wangshu` 的零外部依赖
// 纪律(同 benchmarks/ 子模块做法)。spike 是临时验证,数据进
// docs/design/p3-wasm-tier/implementation-progress.md 后本目录可保留作回归。
package p3boundary
