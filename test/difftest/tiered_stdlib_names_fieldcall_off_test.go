//go:build wangshu_p4 && wangshu_profile && arm64

package difftest

// missingFieldCallRunsCompiled is false for P4 on arm64: its translator is native-only, with no
// head-op replay fallback, and AnalyzeNative accepts a GETTABLE only when its inline cache holds a
// hit, which a read of a missing field never produces. The function stays in the interpreter.
const missingFieldCallRunsCompiled = false
