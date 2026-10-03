//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile && (amd64 || !wangshu_p4)

package regression

// compiledCallersPromote reports whether this build compiles the callers of
// TestHostBoundaryErrorsInCompiledCallers. P3 does on every architecture, and P4 does on amd64,
// where shapes the native translator rejects still compile through the head-op replay path.
const compiledCallersPromote = true
