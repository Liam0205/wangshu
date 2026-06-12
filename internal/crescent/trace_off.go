//go:build !wangshu_trace

package crescent

// traceExec 是逐指令 trace 的编译期开关:默认 false,主循环的 trace 分支
// 被编译器整体消除(零开销,且非全局变量,无并发数据竞争面)。
// 排障时 `go build -tags wangshu_trace` 启用。
const traceExec = false
