//go:build wangshu_oracle_cgo && cgo && !((wangshu_p3 || wangshu_p4) && (amd64 || !wangshu_p4))

package fuzz_test

// topPromotes is false without a compiled tier and for P4 off amd64: the arm64 P4 translator is
// native-only, with no head-op replay fallback, and its AnalyzeNative rejects top (as amd64's
// does), so top stays in the interpreter and State.Call never takes the enterGibbous branch.
const topPromotes = false
