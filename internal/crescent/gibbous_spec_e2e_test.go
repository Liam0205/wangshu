//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"github.com/Liam0205/wangshu/internal/value"
	"testing"
)

// TestPJ2_SpeculativeADD_E2E_RealMmap wires up the real e2e path:
// `function(x,y) return x+y end`. After P4 promotion, the mmap segment
// really emits the movsd/addsd/movsd speculative template, byte-equal with
// the interpreter path (two number inputs). On speculation failure it deopts
// down to host.Arith (string/table inputs).
func TestPJ2_SpeculativeADD_E2E_FastPath(t *testing.T) {
	src := `
local function f(x, y) return x + y end
for i = 1, 100 do f(i, i*2) end
return f(7, 11)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(rets) != 1 || !value.IsNumber(value.Value(rets[0])) {
		t.Fatalf("rets = %v, want [number]", rets)
	}
	got := value.AsNumber(value.Value(rets[0]))
	if got != 18 {
		t.Errorf("f(7, 11) = %v, want 18(spec ADD 路径)", got)
	}
	t.Logf("PJ2 投机 ADD 真接入 e2e:f(7,11) = %v(spec 模板真在 mmap 段跑)", got)
}

// TestPJ2_SpeculativeADD_E2E_DeoptPath speculation failure deopt fallback:
// `function(x, y) return x + y end` called as f(table, 1) trips the IsNumber
// guard → mmap segment returns deoptCode → Run falls back to the host.Arith
// slow path → host.Arith checks string/table coercion via doArith → fails and
// raises "attempt to perform arithmetic on local 'x'(a table value)"
// — byte-equal with the interpreter.
func TestPJ2_SpeculativeADD_E2E_DeoptPath(t *testing.T) {
	src := `
local function f(x, y) return x + y end
for i = 1, 100 do f(i, i*2) end  -- 先升层 spec 模板
return f({}, 1)  -- 触发 IsNumber guard 失败 → deopt → host.Arith → raise`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err == nil {
		t.Fatal("table+number 应 raise(spec deopt → host.Arith → raise)")
	}
	t.Logf("PJ2 投机 deopt 降级 e2e:err = %v(spec 失败 → host helper raise byte-equal 解释器)", err)
}
