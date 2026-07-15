//go:build wangshu_p4

// Package arm64 implements the P4 arm64 backend emitter.
//
// **PJ8 bring-up version** (2026-06-25): linux/arm64 mmap+W^X+codepage done;
// darwin/arm64 W^X (MAP_JIT + pthread_jit_write_protect_np) is left as a PJ8+
// spike; the full emitter template family is left to PJ8+ and pushed
// incrementally in the same way as amd64.
//
// **Cross-platform build strategy of this package**:
//   - linux/arm64: codepage_linux.go (real implementation)
//   - darwin/arm64 / freebsd/arm64 etc.: codepage_other.go (stub MmapCode
//     returns an error)
//   - non-arm64 platforms (amd64 etc.): codepage_nonarm64.go (a placeholder so
//     a wangshu_p4 build still compiles on an amd64 host — the arm64 subpackage
//     is only meaningful when GOARCH=arm64)
//
// Register convention (`docs/design/p4-method-jit/06-backends.md` §4.2):
//   - x28: jitContext (resident; special handling for the Go runtime G register)
//   - x27: arena base
//   - x26: value stack base
//   - x0-x9: scratch
//   - v0-v3: f64 fast path
//
// macOS arm64 W^X (MAP_JIT + pthread_jit_write_protect_np): no wazero
// precedent, self-funded PJ8+ spike (05 §2.2.4).
package arm64
