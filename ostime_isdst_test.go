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

// TestOSTimeIsDST_DefaultHourFallback pins the fallback used when the search finds no
// neighbour with a different offset: glibc applies a DEFAULT one hour rather than treating
// isdst as a no-op.
//
// This is the dominant case by a wide margin. Measured against a C mktime reference over
// 4400 (zone, year, month, isdst) combinations across 220 zones, returning "not found" here
// accounted for 1108 of 1120 mismatches -- every one isdst=true and off by exactly an hour.
// Applying the default takes the total to 44.
//
// The residual 44 sit in 20 zones that abolished DST by keeping the summer offset
// (Africa/Windhoek, America/Cancun, America/Chihuahua and similar), where glibc finds a
// historical neighbour and uses a delta of zero. Whether it finds one depends on its
// transition table, which Go does not expose -- that is the registered exemption, and this
// test documents the measured rate so the trade is visible rather than implied.
func TestOSTimeIsDST_DefaultHourFallback(t *testing.T) {
	loc, err := time.LoadLocation("UTC")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	origLocal := time.Local
	t.Setenv("TZ", "UTC")
	time.Local = loc
	t.Cleanup(func() { time.Local = origLocal })

	// UTC has no DST at all, and glibc still shifts isdst=true by an hour.
	plain := runOne(t, `return tostring(os.time{year=2024,month=1,day=15,hour=12,min=0,sec=0})`).Str()
	dst := runOne(t, `return tostring(os.time{year=2024,month=1,day=15,hour=12,min=0,sec=0,isdst=true})`).Str()
	if plain != "1705320000" {
		t.Errorf("plain = %s, want 1705320000", plain)
	}
	if dst != "1705316400" {
		t.Errorf("isdst=true = %s, want 1705316400 (one hour earlier)", dst)
	}
}

// TestOSTimeIsDST_TwoStateNeighbourDirection pins that the two offsets come from
// searching OUTWARD from the requested instant, which is glibc mktime's direction.
//
// Scanning forward from January 1 instead picks the wrong neighbour whenever the
// offsets changed within the year: America/Vancouver 2026 switches to MST on Nov 1,
// whose offset magnitude equals PDT's, so glibc's nearest non-DST neighbour is MST
// while a January-first scan finds PST an hour further out. Nothing covered the
// two-state class before -- the earlier cases were all Europe/London.
func TestOSTimeIsDST_TwoStateNeighbourDirection(t *testing.T) {
	loc, err := time.LoadLocation("America/Vancouver")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	origLocal := time.Local
	t.Setenv("TZ", "America/Vancouver")
	time.Local = loc
	t.Cleanup(func() { time.Local = origLocal })

	const src = `return tostring(os.time{year=2026,month=8,day=15,hour=12,min=0,sec=0,isdst=false})`
	if got := runOne(t, src).Str(); got != "1786820400" {
		t.Errorf("got %s, want 1786820400 (the MST neighbour, not PST)", got)
	}
}

// TestOSTimeIsDST_StrideMatchesGlibc pins the SEARCH GRANULARITY, not just its
// direction.
//
// glibc's mktime probes in 601200-second strides (about 6.96 days), backward then
// forward at each stride, so it can step past a nearer transition and settle on a
// further zone entry. A one-day scan finds the nearest instead, which differs whenever
// a transition sits inside one stride -- Asia/Anadyr 2010 was an hour off in a
// genuinely two-state year, so the registered single-state exemption did not cover it.
func TestOSTimeIsDST_StrideMatchesGlibc(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Anadyr")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	origLocal := time.Local
	t.Setenv("TZ", "Asia/Anadyr")
	time.Local = loc
	t.Cleanup(func() { time.Local = origLocal })

	const src = `return tostring(os.time{year=2010,month=7,day=15,hour=12,min=0,sec=0,isdst=false})`
	if got := runOne(t, src).Str(); got != "1279152000" {
		t.Errorf("got %s, want 1279152000 (glibc's stride neighbour, not the nearest)", got)
	}
}

// TestOSTimeIsDST_SearchBound pins glibc's search BOUND, which the stride test does not:
// Asia/Anadyr 2010 resolves within 30 strides, so a too-small cap passed it.
//
// glibc's __mktime_internal searches out to delta_bound = duration_max/2 + stride, i.e.
// 447 strides (about 8.5 years). Capping at 30 (~208 days) silently ignored the field in
// two-state years whose nearest opposite-DST instant lies further out -- 131 cases across
// 80 zones, none of them covered by the single-state exemption.
func TestOSTimeIsDST_SearchBound(t *testing.T) {
	for _, tc := range []struct {
		zone, src, want string
	}{
		{"Australia/Perth",
			`return tostring(os.time{year=2006,month=1,day=15,hour=12,min=0,sec=0,isdst=true})`,
			"1137294000"},
		{"America/Havana",
			`return tostring(os.time{year=2006,month=1,day=15,hour=12,min=0,sec=0,isdst=false})`,
			"1137344400"},
		// A 30-minute DST delta, far enough out to need the full bound.
		{"Australia/Lord_Howe",
			`return tostring(os.time{year=1981,month=1,day=15,hour=12,min=0,sec=0,isdst=true})`,
			"348366600"},
	} {
		loc, err := time.LoadLocation(tc.zone)
		if err != nil {
			t.Skip("tzdata unavailable")
		}
		func() {
			origLocal := time.Local
			t.Setenv("TZ", tc.zone)
			time.Local = loc
			defer func() { time.Local = origLocal }()
			if got := runOne(t, tc.src).Str(); got != tc.want {
				t.Errorf("%s: got %s, want %s", tc.zone, got, tc.want)
			}
		}()
	}
}
