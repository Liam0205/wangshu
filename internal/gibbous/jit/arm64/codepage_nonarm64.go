//go:build wangshu_p4 && !arm64

// 非 arm64 平台(amd64 等)的 arm64 子包占位——这里包仍存在但所有 API
// panic,因 arm64 子包仅在 GOARCH=arm64 时才有意义。本文件让 wangshu_p4
// build 在非 arm64 平台仍可编译(避免 internal/gibbous/jit 依赖图缺失)。
package arm64

import "errors"

// CodePage 占位类型(非 arm64 平台)。
type CodePage struct {
	mem    []byte
	length int
}

// MmapCode 在非 arm64 平台返错。
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
