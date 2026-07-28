package wangshu_test

import (
	"testing"
	"time"
)

// TestOSTimeIsDST pins os.time's isdst field against glibc mktime's semantics.
//
// Nothing covered isdst before this, which is how a spring-forward-gap regression
// and a no-DST-zone gap both got through: a single date per zone cannot see either.
// The cases here are the ones a full-year multi-zone sweep flagged.
func TestOSTimeIsDST(t *testing.T) {
	// Fix the zone so the expectations are stable regardless of the host's TZ.
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	// Restore time.Local too, not just TZ: leaving it reassigned would leak into any
	// later TZ-sensitive test in this package.
	origLocal := time.Local
	t.Setenv("TZ", "Europe/London")
	time.Local = loc
	t.Cleanup(func() { time.Local = origLocal })

	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		// Ordinary summer date: isdst=true agrees with the zone, false shifts.
		{"summer default", `return tostring(os.time{year=2024,month=7,day=1,hour=12})`, "1719831600"},
		{"summer isdst=true", `return tostring(os.time{year=2024,month=7,day=1,hour=12,isdst=true})`, "1719831600"},
		{"summer isdst=false", `return tostring(os.time{year=2024,month=7,day=1,hour=12,isdst=false})`, "1719835200"},
		// Winter date: the reverse.
		{"winter default", `return tostring(os.time{year=2024,month=1,day=1,hour=12})`, "1704110400"},
		{"winter isdst=true", `return tostring(os.time{year=2024,month=1,day=1,hour=12,isdst=true})`, "1704106800"},
		// The spring-forward GAP, where the local time does not exist. Go normalizes
		// it forward and reports IsDST()==true; mktime resolves it the other way, so
		// adjusting Go's answer instead of computing from the requested offset was
		// wrong here in both directions.
		{"gap default", `return tostring(os.time{year=2024,month=3,day=31,hour=1,min=30,sec=0})`, "1711848600"},
		{"gap isdst=false", `return tostring(os.time{year=2024,month=3,day=31,hour=1,min=30,sec=0,isdst=false})`, "1711848600"},
		{"gap isdst=true", `return tostring(os.time{year=2024,month=3,day=31,hour=1,min=30,sec=0,isdst=true})`, "1711845000"},
	} {
		if got := runOne(t, tc.src); got.Str() != tc.want {
			t.Errorf("%s: got %v, want %s", tc.name, got.Display(), tc.want)
		}
	}
}

// TestOSTimeIsDST_SingleStateZone pins that isdst is IGNORED where the zone has only
// one DST state.
//
// glibc shifts by a default hour for some such zones and not for others, decided by
// mktime's bounded search of the tzdata transition history, which Go exposes no way to
// reproduce. An unconditional +3600 fallback matched UTC and Asia/Shanghai while
// REGRESSING Africa/Windhoek and Asia/Damascus, which had agreed before -- so the
// narrower behaviour is deliberate and registered in corners_test.go::exemptions.
func TestOSTimeIsDST_SingleStateZone(t *testing.T) {
	loc, err := time.LoadLocation("Africa/Windhoek")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	origLocal := time.Local
	t.Setenv("TZ", "Africa/Windhoek")
	time.Local = loc
	t.Cleanup(func() { time.Local = origLocal })

	plain := runOne(t, `return tostring(os.time{year=2024,month=1,day=15,hour=12,min=30,sec=0})`).Str()
	dst := runOne(t, `return tostring(os.time{year=2024,month=1,day=15,hour=12,min=30,sec=0,isdst=true})`).Str()
	if plain != dst {
		t.Errorf("isdst changed the result in a single-state zone: %s vs %s", plain, dst)
	}
}
