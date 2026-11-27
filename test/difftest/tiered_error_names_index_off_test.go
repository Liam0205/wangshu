//go:build wangshu_p4 && wangshu_profile && arm64

package difftest

// errorNameIndexKernelsRunCompiled is false for P4 on arm64: its translator is native-only, with
// no head-op replay fallback, and AnalyzeNative accepts a table or global access only when its
// inline cache holds a hit, which these kernels never produce. They stay in the interpreter.
const errorNameIndexKernelsRunCompiled = false
