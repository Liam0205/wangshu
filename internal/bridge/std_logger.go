// stdLogger — the default Logger implementation (`docs/design/p2-bridge/04-try-compile-fallback.md` §6.4).
//
// Writes to stderr (io.Writer is injectable, defaulting to os.Stderr); host
// code may inject a custom implementation (structured log / metrics).
package bridge

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// NewStdLogger creates the default Logger writing to the given Writer (nil is equivalent to os.Stderr).
func NewStdLogger(w io.Writer) Logger {
	if w == nil {
		w = os.Stderr
	}
	return &stdLogger{out: w}
}

type stdLogger struct{ out io.Writer }

// LogPromoted logs a successful promotion (T1):
//
//	function <name> promoted to gibbous (entry=<E>, backedge=<B>, feedback=<F>)
func (l *stdLogger) LogPromoted(proto *bytecode.Proto, pd *ProfileData) {
	fmt.Fprintf(l.out, "function %s promoted to gibbous (entry=%d, backedge=%d, feedback=%s)\n",
		protoName(proto), pd.EntryCount, pd.MaxBackEdge(), feedbackSummary(pd.Feedback))
}

// LogStuck logs a function that stays permanently interpreted because it is not compilable (T2):
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

// LogCompileFail logs a compile failure leading to permanent interpretation (T3):
//
//	WARN function <name> compile failed, stays interpreted: <err>
func (l *stdLogger) LogCompileFail(proto *bytecode.Proto, _ *ProfileData, err error) {
	fmt.Fprintf(l.out, "WARN function %s compile failed, stays interpreted: %v\n",
		protoName(proto), err)
}

// LogPanic is the fallback diagnostic for a P3 backend panic (a T3 subtype): the
// full stack goes through a separate channel, while the promotion log emits just one line.
func (l *stdLogger) LogPanic(proto *bytecode.Proto, panicValue interface{}) {
	fmt.Fprintf(l.out, "ERROR function %s P3 backend panic: %v\n%s\n",
		protoName(proto), panicValue, debug.Stack())
}

// silentLogger is a no-op Logger. considerPromotion already handles b.logger == nil
// via a nil check, but sometimes it is preferable to actively inject a Logger that
// "explicitly prints nothing" rather than relying on the nil check (avoiding the
// implicit behavior of a nil interface). Tests can use this Logger to run safely
// without flooding the console.
type silentLogger struct{}

// NewSilentLogger returns a Logger that logs nothing.
func NewSilentLogger() Logger { return silentLogger{} }

func (silentLogger) LogPromoted(_ *bytecode.Proto, _ *ProfileData)               {}
func (silentLogger) LogStuck(_ *bytecode.Proto, _ *ProfileData, _ Compilability) {}
func (silentLogger) LogCompileFail(_ *bytecode.Proto, _ *ProfileData, _ error)   {}
func (silentLogger) LogPanic(_ *bytecode.Proto, _ interface{})                   {}

// protoName returns a readable name for a Proto (preferring Source, falling back to line:Source).
//
// The current Proto has no dedicated Name field (05 §1.7 simplification); the P1
// implementation keeps function-definition info in Source + LineDefined. For the
// full traceback form, see 09.
func protoName(proto *bytecode.Proto) string {
	if proto == nil {
		return "<nil>"
	}
	if proto.Source == "" {
		return fmt.Sprintf("<anonymous>:%d", proto.LineDefined)
	}
	return fmt.Sprintf("%s:%d", proto.Source, proto.LineDefined)
}

// formatReasons renders a ReasonsBitmap into a comma-separated "F<n> <name>" list.
func formatReasons(r ReasonsBitmap) string {
	if r == 0 {
		return "F0 none"
	}
	parts := []string{}
	if r&ReasonVararg != 0 {
		parts = append(parts, "F1 vararg")
	}
	if r&(ReasonYield|ReasonResume|ReasonCoroutine|ReasonUnknownCall|ReasonSelfCall) != 0 {
		// F2 merges several bits into one display — do not report each bit separately, to avoid verbose logs
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

// feedbackSummary condenses a TypeFeedback into the "arith=N mono=M mega=K" format.
//
// Main use: the trailing feedback=<F> field of the promotion log (04 §6.1). Returns "nil" when nil.
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
