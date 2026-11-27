//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile && !(wangshu_p4 && arm64)

package difftest

// errorNameIndexKernelsRunCompiled reports whether force-all runs compiled, on this build, the
// kernels of tieredErrorNameCases that index a nil value or reach a metamethod of the globals
// table. Their table and global accesses never get an inline-cache hit. P3 compiles them on every
// architecture, and P4 does on amd64, where a function the native translator rejects still
// compiles through head-op replay.
const errorNameIndexKernelsRunCompiled = true
