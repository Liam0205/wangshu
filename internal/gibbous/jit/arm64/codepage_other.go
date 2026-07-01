//go:build wangshu_p4 && arm64 && !linux && !(darwin && cgo)

// 非 linux/arm64 且非 darwin/arm64-cgo 的 arm64 codepage 占位 stub。
//
// 路径分布(承 tmp/wangshu-p4-todo.md §三 darwin/arm64 W^X 真实装):
//   - linux/arm64               → codepage_linux.go  (mprotect + 手写 icache flush)
//   - darwin/arm64 && cgo       → codepage_darwin.go (MAP_JIT + pthread_jit_write_protect_np + sys_icache_invalidate)
//   - darwin/arm64 && !cgo      → 本 stub(cross-build 从 linux/amd64 默认 CGO_ENABLED=0)
//   - 其它 GOOS/arm64           → 本 stub
//
// cgo 隔离纪律(选项 I):主库默认 build(CGO_ENABLED=0 cross-build 或
// amd64 主路径)走本 stub,只有 macos-latest CI 启用 cgo 时才链 darwin
// 真实装。
package arm64

import (
	"errors"
	"runtime"
)

type CodePage struct {
	mem    []byte
	length int
}

func MmapCode(code []byte) (*CodePage, error) {
	_ = code
	return nil, errors.New("internal/gibbous/jit/arm64: MmapCode unsupported on " + runtime.GOOS + "/" + runtime.GOARCH + " (CGO_ENABLED=0 stub;darwin/arm64 真实装需 cgo——见 codepage_darwin.go)")
}

func (c *CodePage) Addr() uintptr  { return 0 }
func (c *CodePage) Enter() bool    { return false }
func (c *CodePage) Exit()          {}
func (c *CodePage) Dispose() error { return nil }
func (c *CodePage) Munmap() error  { return nil }
func (c *CodePage) Length() int    { return 0 }
