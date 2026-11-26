//go:build !wangshu_p3 && !wangshu_p4

package tiering_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestGibbousEntries_NoCompiledTier: on a build without a compiled tier the probe stays 0, force-all or not.
func TestGibbousEntries_NoCompiledTier(t *testing.T) {
	prog, err := wangshu.Compile([]byte(`local function kernel(x) return x * 2 end
local s = 0 for i = 1, 40 do s = s + kernel(i) end return s`), "entries")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := st.GibbousEntries(); got != 0 {
		t.Errorf("GibbousEntries = %d on a build without a compiled tier, want 0", got)
	}
}
