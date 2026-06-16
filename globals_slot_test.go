// GlobalsSlot 预解析句柄测试(issue #13 B 件)。验证 SetBySlot/GetBySlot
// 与 SetGlobal/GetGlobal 语义等价、跨 State 误用 panic、Release 卫生。
package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestGlobalsSlot_RoundTripScalar 验证标量(number/bool/string/nil)走 slot
// 写读语义等价于 SetGlobal/GetGlobal。
func TestGlobalsSlot_RoundTripScalar(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	cases := []struct {
		name string
		val  wangshu.Value
		read func(wangshu.Value) any
		want any
	}{
		{"num", wangshu.Number(42.5), func(v wangshu.Value) any { return v.Number() }, 42.5},
		{"flag", wangshu.Bool(true), func(v wangshu.Value) any { return v.Bool() }, true},
		{"name", wangshu.String("alice"), func(v wangshu.Value) any { return v.Str() }, "alice"},
		{"empty", wangshu.Nil(), func(v wangshu.Value) any { return v.IsNil() }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			slot := st.GlobalsSlot(c.name)
			defer slot.Release()
			st.SetBySlot(slot, c.val)
			got := c.read(st.GetBySlot(slot))
			if got != c.want {
				t.Errorf("%s round-trip: got %v want %v", c.name, got, c.want)
			}
		})
	}
}

// TestGlobalsSlot_ScriptVisible 验证经 slot 写入的全局,脚本侧 `GETGLOBAL name`
// 能正常读到——slot 路径与 SetGlobal 写到的就是同一个 globals 表的同一槽。
func TestGlobalsSlot_ScriptVisible(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	priceSlot := st.GlobalsSlot("item_price")
	defer priceSlot.Release()
	rateSlot := st.GlobalsSlot("rate")
	defer rateSlot.Release()
	st.SetBySlot(priceSlot, wangshu.Number(100))
	st.SetBySlot(rateSlot, wangshu.Number(0.85))

	prog, _ := wangshu.Compile([]byte(`return item_price * rate + 10`), "calc")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Number() != 100*0.85+10 {
		t.Errorf("calc = %v, want %v", r[0].Number(), 100*0.85+10)
	}
}

// TestGlobalsSlot_OverwriteAcrossBorrowSimulation 模拟 pineapple LuaOp 复用
// state 的形态:Init 期取一次 slot,Execute 循环里反复改写、读出,且经
// SetGlobal(name, v) 一致写入。
func TestGlobalsSlot_OverwriteAcrossBorrowSimulation(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	slot := st.GlobalsSlot("x")
	defer slot.Release()

	// 通过 slot 反复改写;每次读出应反映最新值
	for i := 0; i < 100; i++ {
		st.SetBySlot(slot, wangshu.Number(float64(i)))
		v := st.GetBySlot(slot)
		if got := v.Number(); got != float64(i) {
			t.Fatalf("iter %d: got %v want %v", i, got, float64(i))
		}
	}

	// slot 写入与 SetGlobal 写入应触达同一 globals 槽
	st.SetBySlot(slot, wangshu.Number(7))
	via := st.GetGlobal("x")
	if got := via.Number(); got != 7 {
		t.Errorf("slot write not visible via GetGlobal: got %v want 7", got)
	}

	st.SetGlobal("x", wangshu.Number(11))
	viaSlot := st.GetBySlot(slot)
	if got := viaSlot.Number(); got != 11 {
		t.Errorf("SetGlobal write not visible via GetBySlot: got %v want 11", got)
	}
}

// TestGlobalsSlot_TableValueWritePersists 验证写 table 类 Value 经 slot 落进
// globals,后续脚本能读出、Release 后表内容仍由 globals 持有保活。
func TestGlobalsSlot_TableValueWritePersists(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	slot := st.GlobalsSlot("xs")
	defer slot.Release()

	tv := st.NewFloatArrayTable([]float64{1, 2, 3, 4, 5})
	st.SetBySlot(slot, tv)
	tv.Release() // 全局表持有,表本身仍可达

	prog, _ := wangshu.Compile([]byte(`
		local s = 0
		for i = 1, #xs do s = s + xs[i] end
		return s
	`), "sum")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Number() != 15 {
		t.Errorf("sum = %v, want 15", r[0].Number())
	}
}

// TestGlobalsSlot_CrossStatePanics 验证跨 State 误用 SetBySlot 触发 panic
// (fail-fast 风格,同 State.Call 跨 State 函数实参)。
func TestGlobalsSlot_CrossStatePanics(t *testing.T) {
	st1 := wangshu.NewState(wangshu.Options{})
	st2 := wangshu.NewState(wangshu.Options{})
	slot1 := st1.GlobalsSlot("x")
	defer slot1.Release()

	t.Run("Set", func(t *testing.T) {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("expected panic on cross-State SetBySlot")
			}
			if msg, ok := r.(string); !ok || !strings.Contains(msg, "different State") {
				t.Errorf("panic msg = %v, want 'different State'", r)
			}
		}()
		st2.SetBySlot(slot1, wangshu.Number(1))
	})

	t.Run("Get", func(t *testing.T) {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("expected panic on cross-State GetBySlot")
			}
			if msg, ok := r.(string); !ok || !strings.Contains(msg, "different State") {
				t.Errorf("panic msg = %v, want 'different State'", r)
			}
		}()
		_ = st2.GetBySlot(slot1)
	})
}

// TestGlobalsSlot_UseAfterReleasePanics 验证 Release 后再用 panic。
func TestGlobalsSlot_UseAfterReleasePanics(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	slot := st.GlobalsSlot("zombie")
	slot.Release()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on use-after-Release")
		}
	}()
	// Release 后 slot.st 置 nil ⟹ 跨 State 校验先 trip("different State");
	// 这是合理的 fail-fast(只要 panic 就达到目的,不强求文本)。
	st.SetBySlot(slot, wangshu.Number(1))
}

// TestGlobalsSlot_DoubleReleaseSafe 验证重复 Release 安全(同 Value.Release)。
func TestGlobalsSlot_DoubleReleaseSafe(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	slot := st.GlobalsSlot("x")
	slot.Release()
	slot.Release() // should be no-op, not panic
}

// TestGlobalsSlot_EmptyName 验证空字符串 name 合法(等价 SetGlobal("", v))。
func TestGlobalsSlot_EmptyName(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	slot := st.GlobalsSlot("")
	defer slot.Release()
	st.SetBySlot(slot, wangshu.Number(99))
	if got := st.GetGlobal("").Number(); got != 99 {
		t.Errorf("globals[''] via slot = %v, want 99", got)
	}
}

// TestGlobalsSlot_SameNameSharedKey 验证同 name 多次取 slot 写到同一 globals
// 槽——两个 slot 内部 GCRef 指向 intern 的同一字符串,落 globals 同一 bucket。
func TestGlobalsSlot_SameNameSharedKey(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	a := st.GlobalsSlot("shared")
	defer a.Release()
	b := st.GlobalsSlot("shared")
	defer b.Release()

	st.SetBySlot(a, wangshu.Number(1))
	if got := st.GetBySlot(b).Number(); got != 1 {
		t.Errorf("slot b read: got %v want 1", got)
	}
	st.SetBySlot(b, wangshu.Number(2))
	if got := st.GetBySlot(a).Number(); got != 2 {
		t.Errorf("slot a read after slot b write: got %v want 2", got)
	}
}

// TestGlobalsSlot_GCStressMode 在 GC 压力下反复 slot 操作,验证 pin 持有的
// name string GCRef 不被回收(否则 SetBySlot/GetBySlot 会读到错绑 arena)。
func TestGlobalsSlot_GCStressMode(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	st.SetGCStressMode(true)
	slot := st.GlobalsSlot("hot_field")
	defer slot.Release()

	for i := 0; i < 200; i++ {
		st.SetBySlot(slot, wangshu.Number(float64(i)))
		if got := st.GetBySlot(slot).Number(); got != float64(i) {
			t.Fatalf("iter %d: got %v want %v", i, got, float64(i))
		}
	}
}
