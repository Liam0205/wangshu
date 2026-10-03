//go:build wangshu_p4 && wangshu_profile && !amd64

package regression

// compiledCallersPromote is false for P4 off amd64: the arm64 translator is native-only, with no
// head-op replay fallback, and its AnalyzeNative rejects most callers of
// TestHostBoundaryErrorsInCompiledCallers (as amd64's does), so they stay in the interpreter and
// the promotion check would fail. The cases still compare the output with lua5.1's.
const compiledCallersPromote = false
