// Package jit 实现 P4 method JIT(gibbous tier-1 第二发射后端)。
//
// **当前状态:PJ0 包骨架**(`docs/design/p4-method-jit/00-overview.md` §4)。
// SupportsAllOpcodes 返 false ⇒ 所有 Proto 仍走 crescent。
// PJ1+ 起 amd64 trampoline + 直线模板 + 算术投机 + OSR exit 渐进交付。
//
// 与 P3(`internal/gibbous/wasm`)的关系:同 tier-1 不同发射后端,共享
// `bridge.P3Compiler` 接口面(`p2-bridge/05-p3-p4-interface.md` §0.3 接口
// 稳定性)——P3 发 wasm 交 wazero 跑、P4 发原生码自管 codegen。Build tag
// 互斥:`wangshu_p3` 与 `wangshu_p4` 不同时启用(主助理裁决,与 P3 单字段
// `b.p3` 注入对齐;PJ10 P3 退役时 P4 完全接手,见 07-p3-retirement.md §5)。
//
// **方案 A 投机生命周期 P4 自管**(`03-speculation-ic.md` §4 / §8 + 用户
// 裁决 d0e57f4):P2 三态枚举 `TierInterp/TierGibbous/TierStuck` 不变;
// P4 在本包内维护 `p4SpecState[proto]` 子状态机(`P4Speculative /
// P4Deoptimized / P4StuckSpeculation`)。OSR exit / 重训练 / 拉黑投机全
// P4 自管,P2 不感知。
//
// 包结构(PJ0 起立桩,后续 PJ 渐进填实):
//   - compiler.go     P3Compiler 实现 + 渐进白名单
//   - code.go         GibbousCode 实装(p4Code struct)
//   - state.go        P4 投机生命周期状态机(p4SpecState)
//   - amd64/          amd64 后端(发射器 + trampoline asm)
//   - arm64/          arm64 后端(同款,PJ8 启动)
//
// 上游契约:
//   - `docs/design/p4-method-jit/00-overview.md` §4 PJ 表 / §6 速查 / §9 不变式
//   - `docs/design/p4-method-jit/02-template-direction.md` 方向裁决
//   - `docs/design/p4-method-jit/05-system-pipeline.md` 系统管线四件套
//   - `docs/design/p4-method-jit/06-backends.md` 双后端切分
package jit
