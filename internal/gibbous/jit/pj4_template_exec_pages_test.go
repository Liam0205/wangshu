//go:build wangshu_p4 && (amd64 || arm64) && (linux || (darwin && arm64 && cgo))

package jit

// tmplExecPagesExpected says that this build has a real archMmapCode: the
// constraint above is the union of those on amd64/codepage_linux.go,
// arm64/codepage_linux.go and arm64/codepage_darwin.go.
const tmplExecPagesExpected = true
