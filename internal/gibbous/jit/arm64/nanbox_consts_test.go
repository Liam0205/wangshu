//go:build wangshu_p4 && arm64

package arm64

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/value"
)

// TestNilImmMatchesValueNil pins the hand-written NaN-box immediates to the
// value package's encoding (see the amd64 twin; issue #260).
func TestNilImmMatchesValueNil(t *testing.T) {
	if qNanBoxNilImmArm64 != uint64(value.Nil) {
		t.Fatalf("qNanBoxNilImmArm64 = %#x, value.Nil = %#x", qNanBoxNilImmArm64, uint64(value.Nil))
	}
	if qNanBoxTableTagShiftedArm64 != uint64(value.TagTable) {
		t.Fatalf("qNanBoxTableTagShiftedArm64 = %#x, TagTable = %#x", qNanBoxTableTagShiftedArm64, value.TagTable)
	}
}
