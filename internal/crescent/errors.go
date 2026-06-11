// Error position annotation + traceback (09)。
package crescent

import (
	"fmt"
	"strings"

	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// annotateError 给运行期错误加 "chunkname:line:" 前缀(09 错误目录措辞)。
//
// 只对"解释器内在错误"(类型错误等)与 error(msg, level≥1) 加;error(v)
// 携带非字符串值或 level=0 时不加(5.1 语义)。同一错误只加一次。
func (st *State) annotateError(e *LuaError, ci *callInfo) *LuaError {
	if e == nil || e == errYieldSentinel || e.annotated {
		return e
	}
	e.annotated = true
	src := ci.proto.Source
	line := int32(0)
	pc := int(ci.pc) - 1
	if pc >= 0 && pc < len(ci.proto.LineInfo) {
		line = ci.proto.LineInfo[pc]
	}
	prefix := fmt.Sprintf("%s:%d: ", src, line)
	e.Msg = prefix + e.Msg
	// 若 Value 是字符串(或未设置),同步加前缀;非字符串错误值保持原样(5.1)
	if e.Value == value.Value(0) || e.Value == value.Nil {
		e.Value = value.MakeGC(value.TagString, st.gc.Intern([]byte(e.Msg)))
	} else if value.Tag(e.Value) == value.TagString && e.Level != 0 {
		raw := object.StringBytes(st.arena, value.GCRefOf(e.Value))
		e.Value = value.MakeGC(value.TagString, st.gc.Intern(append([]byte(prefix), raw...)))
	}
	return e
}

// buildTraceback 构建调用栈回溯(09:chunkname:line + [C] 帧)。
func (st *State) buildTraceback(th *thread) string {
	var sb strings.Builder
	sb.WriteString("stack traceback:")
	for i := len(th.cis) - 1; i >= 0; i-- {
		ci := &th.cis[i]
		sb.WriteString("\n\t")
		if ci.proto == nil {
			sb.WriteString("[C]: in ?")
			continue
		}
		line := int32(0)
		pc := int(ci.pc) - 1
		if pc >= 0 && pc < len(ci.proto.LineInfo) {
			line = ci.proto.LineInfo[pc]
		}
		what := "function"
		if i == 0 {
			what = "main chunk"
		}
		if ci.tailcall {
			sb.WriteString("(...tail calls...)\n\t")
		}
		fmt.Fprintf(&sb, "%s:%d: in %s", ci.proto.Source, line, what)
	}
	return sb.String()
}

// Traceback 暴露给 stdlib(debug.traceback 的 P1 形态)。
func (st *State) Traceback() string {
	if st.runningThread == nil {
		return "stack traceback:"
	}
	return st.buildTraceback(st.runningThread)
}
