// stdLogger — 默认 Logger 实现(`docs/design/p2-bridge/04-try-compile-fallback.md` §6.4)。
//
// 写 stderr(io.Writer 注入式,默认 os.Stderr);宿主代码可注入自定义实现
// (structured log / metrics)。
package bridge

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// NewStdLogger 默认 Logger 写入指定 Writer(nil 等价 os.Stderr)。
func NewStdLogger(w io.Writer) Logger {
	if w == nil {
		w = os.Stderr
	}
	return &stdLogger{out: w}
}

type stdLogger struct{ out io.Writer }

// LogPromoted 升层成功(T1):
//
//	function <name> promoted to gibbous (entry=<E>, backedge=<B>, feedback=<F>)
func (l *stdLogger) LogPromoted(proto *bytecode.Proto, pd *ProfileData) {
	fmt.Fprintf(l.out, "function %s promoted to gibbous (entry=%d, backedge=%d, feedback=%s)\n",
		protoName(proto), pd.EntryCount, pd.MaxBackEdge(), feedbackSummary(pd.Feedback))
}

// LogStuck 不可编译永久解释(T2):
//
//	function <name> stays interpreted (not compilable: F<n> <reason>)
func (l *stdLogger) LogStuck(proto *bytecode.Proto, pd *ProfileData, comp Compilability) {
	reason := "unknown"
	switch comp {
	case CompNotCompilable:
		reason = formatReasons(pd.Reasons)
	case CompUnknown:
		reason = "F0 not analyzed"
	}
	fmt.Fprintf(l.out, "function %s stays interpreted (not compilable: %s)\n",
		protoName(proto), reason)
}

// LogCompileFail 编译失败永久解释(T3):
//
//	WARN function <name> compile failed, stays interpreted: <err>
func (l *stdLogger) LogCompileFail(proto *bytecode.Proto, _ *ProfileData, err error) {
	fmt.Fprintf(l.out, "WARN function %s compile failed, stays interpreted: %v\n",
		protoName(proto), err)
}

// LogPanic P3 后端 panic 兜底诊断(T3 子类):完整 stack 走独立 channel,
// 升层日志只说一行。
func (l *stdLogger) LogPanic(proto *bytecode.Proto, panicValue interface{}) {
	fmt.Fprintf(l.out, "ERROR function %s P3 backend panic: %v\n%s\n",
		protoName(proto), panicValue, debug.Stack())
}

// silentLogger 是无操作 Logger——considerPromotion 在 b.logger == nil 下走
// nil 检查,但有时希望主动注入「明确不打印」的 Logger 而非依赖 nil
// 检查(避免 nil 接口的隐式行为)。测试可用此 Logger 安全跑而不刷屏。
type silentLogger struct{}

// NewSilentLogger 返回一个不打任何日志的 Logger。
func NewSilentLogger() Logger { return silentLogger{} }

func (silentLogger) LogPromoted(_ *bytecode.Proto, _ *ProfileData)               {}
func (silentLogger) LogStuck(_ *bytecode.Proto, _ *ProfileData, _ Compilability) {}
func (silentLogger) LogCompileFail(_ *bytecode.Proto, _ *ProfileData, _ error)   {}
func (silentLogger) LogPanic(_ *bytecode.Proto, _ interface{})                   {}

// protoName 取 Proto 的可读名字(优先 Source,降级 line:Source)。
//
// 当前 Proto 没有独立的 Name 字段(05 §1.7 简化),P1 实装把函数定义信息
// 留在 Source + LineDefined。完整 traceback 形态参见 09。
func protoName(proto *bytecode.Proto) string {
	if proto == nil {
		return "<nil>"
	}
	if proto.Source == "" {
		return fmt.Sprintf("<anonymous>:%d", proto.LineDefined)
	}
	return fmt.Sprintf("%s:%d", proto.Source, proto.LineDefined)
}

// formatReasons 把 ReasonsBitmap 翻成 "F<n> <name>" 列表(逗号分隔)。
func formatReasons(r ReasonsBitmap) string {
	if r == 0 {
		return "F0 none"
	}
	parts := []string{}
	if r&ReasonVararg != 0 {
		parts = append(parts, "F1 vararg")
	}
	if r&(ReasonYield|ReasonResume|ReasonCoroutine|ReasonUnknownCall|ReasonSelfCall) != 0 {
		// F2 多个位合并显示——不分别报每位,避免日志冗长
		parts = append(parts, "F2 "+formatF2(r))
	}
	if r&ReasonDebug != 0 {
		parts = append(parts, "F3 debug")
	}
	if r&ReasonSetfenv != 0 {
		parts = append(parts, "F4 setfenv")
	}
	if r&(ReasonOverSize|ReasonOverRegs) != 0 {
		parts = append(parts, "F5 "+formatF5(r))
	}
	if r&(ReasonNestedDeep|ReasonOverUpval) != 0 {
		parts = append(parts, "F6 "+formatF6(r))
	}
	if r&ReasonBackendUnsupp != 0 {
		parts = append(parts, "F7 backendUnsupp")
	}
	return strings.Join(parts, ", ")
}

func formatF2(r ReasonsBitmap) string {
	parts := []string{}
	if r&ReasonYield != 0 {
		parts = append(parts, "yield")
	}
	if r&ReasonResume != 0 {
		parts = append(parts, "resume")
	}
	if r&ReasonCoroutine != 0 && r&(ReasonYield|ReasonResume) == 0 {
		parts = append(parts, "coroutine.*")
	}
	if r&ReasonUnknownCall != 0 {
		parts = append(parts, "unknownCall")
	}
	if r&ReasonSelfCall != 0 {
		parts = append(parts, "selfCall")
	}
	return strings.Join(parts, "+")
}

func formatF5(r ReasonsBitmap) string {
	parts := []string{}
	if r&ReasonOverSize != 0 {
		parts = append(parts, "oversize")
	}
	if r&ReasonOverRegs != 0 {
		parts = append(parts, "overregs")
	}
	return strings.Join(parts, "+")
}

func formatF6(r ReasonsBitmap) string {
	parts := []string{}
	if r&ReasonNestedDeep != 0 {
		parts = append(parts, "nestedDeep")
	}
	if r&ReasonOverUpval != 0 {
		parts = append(parts, "overupval")
	}
	return strings.Join(parts, "+")
}

// feedbackSummary 把 TypeFeedback 简略统计成「arith=N mono=M mega=K」格式。
//
// 主用途:升层日志末段 feedback=<F> 字段(04 §6.1)。nil 时返 "nil"。
func feedbackSummary(fb *TypeFeedback) string {
	if fb == nil {
		return "nil"
	}
	var arith, mono, mega int
	for _, p := range fb.Points {
		switch p.Kind {
		case FBArithStableNumber:
			arith++
		case FBTableMono, FBGlobalStable, FBSelfMono:
			mono++
		case FBTableMega:
			mega++
		}
	}
	return fmt.Sprintf("arith=%d mono=%d mega=%d", arith, mono, mega)
}
