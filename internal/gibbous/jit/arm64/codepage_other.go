//go:build wangshu_p4 && arm64 && !linux && !(darwin && cgo)

// Placeholder stub for arm64 codepage on non-linux/arm64 and non-darwin/arm64-cgo targets.
//
// Path distribution (per tmp/wangshu-p4-todo.md §3 darwin/arm64 W^X real implementation):
//   - linux/arm64               → codepage_linux.go  (mprotect + hand-written icache flush)
//   - darwin/arm64 && cgo       → codepage_darwin.go (MAP_JIT + pthread_jit_write_protect_np + sys_icache_invalidate)
//   - darwin/arm64 && !cgo      → this stub (cross-build from linux/amd64 with default CGO_ENABLED=0)
//   - other GOOS/arm64          → this stub
//
// cgo isolation discipline (option I): the default main-library build (CGO_ENABLED=0
// cross-build or the amd64 main path) uses this stub; only when macos-latest CI enables
// cgo does it link the real darwin implementation.
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
