//go:build wangshu_p4 && !arm64

// Placeholder for the arm64 subpackage on non-arm64 platforms (amd64, etc.) —
// the package still exists here but all APIs panic, because the arm64 subpackage
// only makes sense when GOARCH=arm64. This file lets the wangshu_p4 build still
// compile on non-arm64 platforms (avoiding a missing node in internal/gibbous/jit's
// dependency graph).
package arm64

import "errors"

// CodePage placeholder type (non-arm64 platforms).
type CodePage struct {
	mem    []byte
	length int
}

// MmapCode returns an error on non-arm64 platforms.
func MmapCode(code []byte) (*CodePage, error) {
	_ = code
	return nil, errors.New("internal/gibbous/jit/arm64: only available on GOARCH=arm64")
}

// Stubs for the refcounted API on non-arm64 platforms.
func (c *CodePage) Addr() uintptr  { return 0 }
func (c *CodePage) Enter() bool    { return false }
func (c *CodePage) Exit()          {}
func (c *CodePage) Dispose() error { return nil }
func (c *CodePage) Munmap() error  { return nil }
func (c *CodePage) Length() int    { return 0 }
