//go:build wangshu_p4

// Package arm64 实装 P4 arm64 后端发射器。
//
// **PJ8 启动版**(2026-06-25):linux/arm64 mmap+W^X+codepage 落地;
// darwin/arm64 W^X(MAP_JIT + pthread_jit_write_protect_np)留 PJ8+ spike;
// 完整 emitter 模板族留 PJ8+ 与 amd64 同款渐进推。
//
// **本包跨平台 build 策略**:
//   - linux/arm64:codepage_linux.go(真实装)
//   - darwin/arm64 / freebsd/arm64 等:codepage_other.go(stub MmapCode 返错)
//   - 非 arm64 平台(amd64 等):codepage_nonarm64.go(占位让 wangshu_p4 build
//     在 amd64 主机仍可编译——arm64 子包仅在 GOARCH=arm64 才有真意义)
//
// 寄存器约定(`docs/design/p4-method-jit/06-backends.md` §4.2):
//   - x28:jitContext(常驻;Go runtime 的 G 寄存器特殊处理)
//   - x27:arena base
//   - x26:值栈 base
//   - x0-x9:暂存
//   - v0-v3:f64 快路径
//
// macOS arm64 W^X(MAP_JIT + pthread_jit_write_protect_np):无 wazero 实装
// 先例,PJ8+ 自付 spike(05 §2.2.4)。
package arm64
