// Package conformance hosts ported Lua 5.1 conformance scripts and the harness
// that runs them against Wangshu. See docs/design/p1-interpreter/12-testing-difftest.md.
//
// Currently a placeholder: M14 will land the conformance harness and the first batch of cases; until
// then the package contains only doc.go, so that `go test ./test/conformance/...` passes directly in CI
// (empty-package semantics, engineering.md §3.1).
package conformance
