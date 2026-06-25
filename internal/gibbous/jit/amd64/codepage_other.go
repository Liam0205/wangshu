//go:build wangshu_p4 && !(linux && amd64)

// 非 linux/amd64 的 codepage 占位 stub。PJ1 阶段只做 linux/amd64;darwin/arm64
// 的 W^X 形态(MAP_JIT + pthread_jit_write_protect_np)留 PJ8 启动前 spike +
// 实装(承 docs/design/p4-method-jit/05-system-pipeline.md §2.2.4)。
//
// 在 PJ1 范围内,本文件保证 wangshu_p4 build 在非 linux/amd64 平台仍可编译
// (CodePage / MmapCode 类型存在,但调用即 panic——这与「PJ1 验收平台 = amd64」
// 对齐,06 §6.1 PJ1 行)。
package amd64

import (
	"errors"
	"runtime"
)

// CodePage 占位类型(非 linux/amd64 平台)。
type CodePage struct {
	mem    []byte
	length int
}

// MmapCode 在非 linux/amd64 平台上返错——PJ1 阶段不支持。
func MmapCode(code []byte) (*CodePage, error) {
	_ = code
	return nil, errors.New("internal/gibbous/jit/amd64: MmapCode unsupported on " + runtime.GOOS + "/" + runtime.GOARCH + " (PJ1 only on linux/amd64; darwin/arm64 留 PJ8)")
}

// Addr 占位返 0。
func (c *CodePage) Addr() uintptr { return 0 }

// Munmap 占位 no-op。
func (c *CodePage) Munmap() error { return nil }

// Length 占位返 0。
func (c *CodePage) Length() int { return 0 }
