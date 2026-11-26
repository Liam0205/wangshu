//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile && !(wangshu_p4 && arm64)

package difftest

// missingFieldCallRunsCompiled reports whether force-all runs a function that reads a missing table
// field compiled on this build. P3 does on every architecture, and P4 does on amd64, where a function
// the native translator rejects still compiles through head-op replay.
const missingFieldCallRunsCompiled = true
