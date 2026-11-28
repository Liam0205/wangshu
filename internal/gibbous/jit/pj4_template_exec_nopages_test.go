//go:build wangshu_p4 && (amd64 || arm64) && !(linux || (darwin && arm64 && cgo))

package jit

// tmplExecPagesExpected is false where archMmapCode is the stub that returns
// an error (amd64/codepage_other.go, arm64/codepage_other.go).
const tmplExecPagesExpected = false
