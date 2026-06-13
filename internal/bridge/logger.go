// Logger — 升层日志诊断接口(`docs/design/p2-bridge/04-try-compile-fallback.md` §6.4)。
//
// 接口最小化:P2 PB0 占位,PB5 落地真实装。本文件先建接口形状以稳契约。
package bridge

import "github.com/Liam0205/wangshu/internal/bytecode"

// Logger 是 P2 升层日志诊断接口(04 §6.4)。
//
// **接口稳定性承诺**(04 §6.4 + 05 §0.3):这四个方法在 P2 上线后**不修改
// 签名**——P3/P4 只能新增接口(嵌入 Logger),不能改本接口。
type Logger interface {
	// LogPromoted: T1 转移成功——升层成功日志(04 §6.1)。
	// 格式:function <name> promoted to gibbous (entry=<E>, backedge=<B>, feedback=<F>)
	LogPromoted(proto *bytecode.Proto, pd *ProfileData)

	// LogStuck: T2 转移——不可编译永久解释(04 §6.2)。
	// 格式:function <name> stays interpreted (not compilable: F<n> <reason>)
	LogStuck(proto *bytecode.Proto, pd *ProfileData, comp Compilability)

	// LogCompileFail: T3 转移——编译失败永久解释(04 §6.3)。
	// 格式:function <name> compile failed, stays interpreted: <err>
	LogCompileFail(proto *bytecode.Proto, pd *ProfileData, err error)

	// LogPanic: T3 子类——P3 后端 panic 兜底诊断(04 §5.2)。
	// 升层日志只说一行(T3 LogCompileFail);完整 stack 走此独立 channel。
	LogPanic(proto *bytecode.Proto, panicValue interface{})
}
