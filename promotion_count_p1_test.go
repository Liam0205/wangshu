//go:build !wangshu_p3

// promotion_count_p1_test.go: verifies State.PromotionCount() always returns 0
// under the p1 build / when P3 is not injected (godoc promises a no-op equivalent).
//
// For the p3 build, see promotion_count_p3_test.go.
package wangshu_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

func TestPromotionCount_P1_AlwaysZero(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true) // no-op under p1 as well

	prog, err := wangshu.Compile([]byte(`return 1+2`), "test")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := st.PromotionCount(); got != 0 {
		t.Errorf("p1 build PromotionCount = %d, want 0(p1 build 永远 no-op)", got)
	}
}
