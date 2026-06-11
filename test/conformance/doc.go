// Package conformance hosts ported Lua 5.1 conformance scripts and the harness
// that runs them against Wangshu. See docs/design/p1-interpreter/12-testing-difftest.md.
//
// 当前为占位:M14 落地 conformance harness 与首批用例;此前包内仅 doc.go,使
// `go test ./test/conformance/...` 在 CI 直接 PASS(空包语义,engineering.md §3.1)。
package conformance
