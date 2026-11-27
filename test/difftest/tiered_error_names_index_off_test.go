//go:build wangshu_p4 && wangshu_profile && arm64

package difftest

// errorNameIndexKernelsRunCompiled is false for P4 on arm64: its translator is native-only, with
// no head-op replay fallback, and AnalyzeNative accepts a function only when the inline cache of
// every table and global access in it holds a hit. Each of these kernels has one access, the
// faulting one, whose cache never does, so they stay in the interpreter.
const errorNameIndexKernelsRunCompiled = false
