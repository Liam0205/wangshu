// 错误消息差分(12 §4 错误措辞口径):pcall 捕获的 errmsg 与官方 5.1.5 对拍。
//
// 对拍规则:errmsg 含 "chunkname:line:" 前缀——两边 chunkname 不同(wangshu
// 用 chunkname 参数,oracle 经 stdin 是 "stdin"),归一化:截掉首个 ": " 之前
// 的 chunkname 段,行号段与正文分别断言一致。
package difftest

import (
	"regexp"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// errCase 是一个错误用例:脚本经 pcall 包裹,return tostring(errmsg)。
type errCase struct {
	name string
	expr string // 出错表达式(在函数体内执行)
}

var errCorpus = []errCase{
	// 算术类型错误
	{"arith_on_nil", `local x; return x + 1`},
	{"arith_on_string_bad", `return "zz" + 1`},
	{"arith_on_table", `return {} + 1`},
	{"arith_on_bool", `return true + 1`},
	{"unm_on_table", `return -({})`},
	// 调用错误
	{"call_nil", `local f; return f()`},
	{"call_number", `local f = 42; return f()`},
	{"call_string", `local f = "s"; return f()`},
	// 索引错误
	{"index_nil", `local t; return t.x`},
	{"index_number", `local t = 5; return t.x`},
	{"index_boolean", `local t = true; return t.x`},
	{"newindex_nil", `local t; t.x = 1`},
	// 比较错误
	{"compare_num_str", `return 1 < "x"`},
	{"compare_bool_bool", `return true < false`},
	{"compare_table_num", `return {} < 1`},
	// 长度错误
	{"len_on_number", `return #42`},
	{"len_on_nil", `local x; return #x`},
	{"len_on_bool", `return #true`},
	// 拼接错误
	{"concat_nil", `local x; return "a" .. x`},
	{"concat_table", `return "a" .. {}`},
	{"concat_bool", `return "a" .. true`},
	// for 错误
	{"for_init_string", `for i = "x", 10 do end`},
	{"for_limit_table", `for i = 1, {} do end`},
	{"for_step_nil", `local s; for i = 1, 10, s do end`},
	// table 索引键错误
	{"table_index_nil_key", `local t = {}; local k; t[k] = 1`},
	{"table_index_nan_key", `local t = {}; t[0/0] = 1`},
	// upvalue 名字形态(expr 内再嵌一层函数,外层局部被捕获为 upvalue;
	// 官方 getobjname 走 getupvalname 分支报 "upvalue 'x'")
	{"arith_on_upvalue", `local up; local f = function() return up + 1 end; return f()`},
	{"call_upvalue", `local upf; local f = function() return upf() end; return f()`},
	{"index_upvalue", `local upt; local f = function() return upt.k end; return f()`},
}

// stripPos 截掉 "chunkname:line: " 位置前缀,返回 (line 段, 余下消息)。
var posRe = regexp.MustCompile(`^[^:]+:(\d+): (.*)$`)

func stripPos(msg string) (string, string) {
	m := posRe.FindStringSubmatch(strings.TrimSpace(msg))
	if m == nil {
		return "", strings.TrimSpace(msg)
	}
	return m[1], m[2]
}

// TestDiff_ErrorMessages 对拍错误消息:正文逐字节 + 行号段也断言一致
// (两边脚本结构相同,行号应相等;LineInfo 错位类 bug 经此防线)。
func TestDiff_ErrorMessages(t *testing.T) {
	oracle := findOracle()
	if oracle == "" {
		t.Skip("lua5.1 oracle not found on PATH; skipping difftest")
	}
	for _, c := range errCorpus {
		t.Run(c.name, func(t *testing.T) {
			script := "local ok, err = pcall(function()\n" + c.expr + "\nend)\nreturn tostring(err)"
			// oracle
			oracleSrc := wrapForOracle(script)
			wantRaw := runOracle(t, oracle, oracleSrc)
			wantLine, want := stripPos(wantRaw)
			// wangshu
			prog, err := wangshu.Compile([]byte(script), "errdiff")
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			st := wangshu.NewState(wangshu.Options{})
			results, err := prog.Run(st)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			gotLine, got := stripPos(results[0].GoString())
			if got != want {
				t.Errorf("error message diff:\n  wangshu: %q\n  oracle:  %q", got, want)
			}
			if gotLine != wantLine {
				t.Errorf("error line diff: wangshu %q vs oracle %q (msg %q)", gotLine, wantLine, want)
			}
		})
	}
}
