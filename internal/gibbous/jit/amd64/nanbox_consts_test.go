//go:build wangshu_p4 && amd64

package amd64

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/value"
)

// TestNilImmMatchesValueNil pins the hand-written NaN-box immediates to the
// value package's encoding. The emitter cannot import internal/value, so a
// drift here is otherwise invisible until a fuzz seed finds a Nil slot that
// the inline guard lets through (issue #260: 0xFFFE<<48 is TagUserdata).
func TestNilImmMatchesValueNil(t *testing.T) {
	if qNanBoxNilImm != uint64(value.Nil) {
		t.Fatalf("qNanBoxNilImm = %#x, value.Nil = %#x", qNanBoxNilImm, uint64(value.Nil))
	}
	if qNanBoxTableTagShifted != uint64(value.TagTable)<<48 {
		t.Fatalf("qNanBoxTableTagShifted = %#x, TagTable<<48 = %#x", qNanBoxTableTagShifted, uint64(value.TagTable)<<48)
	}
	if uint16(qNanBoxTableTagHigh16) != value.TagTable {
		t.Fatalf("qNanBoxTableTagHigh16 = %#x, TagTable = %#x", qNanBoxTableTagHigh16, value.TagTable)
	}
}
