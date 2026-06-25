//go:build wangshu_p4 && arm64 && !linux

// 非 linux/arm64 的 arm64 codepage 占位 stub。darwin/arm64 W^X 形态
// (MAP_JIT + pthread_jit_write_protect_np)留 PJ8+ 启动前 spike。
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
	return nil, errors.New("internal/gibbous/jit/arm64: MmapCode unsupported on " + runtime.GOOS + "/" + runtime.GOARCH + " (PJ8 启动版仅 linux/arm64;darwin/arm64 留 PJ8+ spike)")
}

func (c *CodePage) Addr() uintptr { return 0 }
func (c *CodePage) Munmap() error { return nil }
func (c *CodePage) Length() int   { return 0 }
