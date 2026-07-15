//go:build wangshu_p4 && !(linux && amd64)

// Placeholder codepage stub for non-linux/amd64. PJ1 only targets linux/amd64;
// the W^X form for darwin/arm64 (MAP_JIT + pthread_jit_write_protect_np) is
// left for a spike + implementation before PJ8 starts (per
// docs/design/p4-method-jit/05-system-pipeline.md §2.2.4).
//
// Within the PJ1 scope, this file ensures the wangshu_p4 build still compiles on
// non-linux/amd64 platforms (the CodePage / MmapCode types exist, but calling
// them panics — which aligns with "PJ1 acceptance platform = amd64", the PJ1
// row of 06 §6.1).
package amd64

import (
	"errors"
	"runtime"
)

// CodePage stub type (non-linux/amd64 platforms).
type CodePage struct {
	mem    []byte
	length int
}

// MmapCode returns an error on non-linux/amd64 platforms; PJ1 does not
// support mmap+W^X off linux/amd64 (darwin/arm64 lives in the arm64
// sub-package with its own MAP_JIT + pthread_jit_write_protect_np path).
func MmapCode(code []byte) (*CodePage, error) {
	_ = code
	return nil, errors.New("internal/gibbous/jit/amd64: MmapCode unsupported on " + runtime.GOOS + "/" + runtime.GOARCH + " (PJ1 only on linux/amd64; darwin/arm64 in arm64 sub-package)")
}

// Addr stub returns 0.
func (c *CodePage) Addr() uintptr { return 0 }

// Enter stub returns false (no page ever allocated on non-linux/amd64).
func (c *CodePage) Enter() bool { return false }

// Exit stub is a no-op.
func (c *CodePage) Exit() {}

// Dispose stub no-op.
func (c *CodePage) Dispose() error { return nil }

// Munmap stub no-op (kept for backwards compat with Dispose).
func (c *CodePage) Munmap() error { return nil }

// Length stub returns 0.
func (c *CodePage) Length() int { return 0 }
