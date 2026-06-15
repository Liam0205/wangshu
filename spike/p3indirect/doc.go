// Package p3indirect is the PW10 spike harness for eliminating the
// gibbous→gibbous cross-layer call tax (docs/design/p3-wasm-tier/04-trampoline.md
// §9 缺口 + 02-translation.md §1.2).
//
// 背景:P3 PW9 实测发现 gibbous→gibbous 调用经 h_call 双跨层(Wasm→Go→Wasm,
// PW0 实测 ~143ns/次)使调用密集核退化(call 核比 crescent 慢 7x)。真解是
// 「单 module + 内部函数表 + call_indirect 直调」——但这是里程碑级架构改,被
// 两条码库 physics 卡死(每 Proto 独立 module + Lua 帧住 Go),且有一个生死
// 未知数:wazero 能否增量编译/重实例化 module(增量升层需要)。
//
// 本 spike 是 PW10 的开工闸门(对齐 PW0 先例),验两个生死假设:
//
//   - S-A:单 module 内 call_indirect 单次成本——必须 ≪143ns(host 往返),
//     目标 <30ns(近原生 indirect call)。不达标 ⇒ 整个大修无意义。
//   - S-B:增量升层的 module 重编成本 + 实例热交换生命周期——验证「重编
//     module{A} → module{A,B} 并安全切换实例」可行(旧实例上有 in-flight
//     gibbous 帧时不能 Close)。
//
// 对照基线 S-Host 复刻 PW0 S3N 的 imported 调用单次摊销成本(~143ns),供
// S-A 直接对比加速倍率。
//
// **独立 go module**:不污染主库零外部依赖纪律(同 spike/p3boundary + benchmarks/)。
// 数据进 docs/design/p3-wasm-tier/implementation-progress.md §11 PW10 决策后,
// 本目录保留作回归。
package p3indirect
