package wangshu_test

import (
	"runtime"
	"strconv"
	"testing"
	"time"
)

// requireGlibcMktime skips a test whose expectations were derived from glibc's mktime.
//
// The isdst implementation copies glibc's transition search (stride, probe order, bound,
// fallback deltas), and those choices are libc-specific: macOS uses BSD libc, whose search
// differs, so the "correct" epoch second for these edge cases is not the same there. The
// cases below assert glibc's answers, verified against a C mktime reference on Linux.
//
// These cases pin glibc's exact epoch seconds, so they only run on Linux. They are NOT the
// portability contract -- an earlier version of this comment claimed the product would agree
// with a host-libc oracle on BSD, which is false: the product uses glibc's rules everywhere
// and would disagree with a locally built PUC there. TestOSTimeIsDST_PortableContract below
// asserts what actually holds on every platform, and runs everywhere.
func requireGlibcMktime(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("expectations derived from glibc mktime; this platform's libc searches differently")
	}
}

// TestOSTimeIsDST pins os.time's isdst field against glibc mktime's semantics.
//
// Nothing covered isdst before this, which is how a spring-forward-gap regression
// and a no-DST-zone gap both got through: a single date per zone cannot see either.
// The cases here are the ones a full-year multi-zone sweep flagged.
func TestOSTimeIsDST(t *testing.T) {
	requireGlibcMktime(t)
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

// TestOSTimeIsDST_DefaultHourFallback pins the two coupled decisions that took the isdst
// implementation from a 1% residual to exact agreement.
//
// When the outward search finds NO neighbour at all, glibc falls back to a default one
// hour -- that is the dominant term, worth 1085 of 1120 mismatches on its own. When it
// finds a neighbour whose offset EQUALS the base, glibc's delta for it is ZERO, so isdst
// has no effect; treating that as the default hour instead was the entire remaining
// residual.
//
// The two are coupled with maxStrides: with the equal-offset delta wrong, narrowing the
// bound measured as neutral-to-worse, which is why several rounds could not settle either
// alone. Measured against a C mktime reference: 0 of 4400 across 220 zones, and 0 of 6600
// on an independent holdout of different zones, years and months.
func TestOSTimeIsDST_DefaultHourFallback(t *testing.T) {
	requireGlibcMktime(t)
	for _, tc := range []struct {
		zone, plainWant, dstWant, why string
	}{
		// No neighbour found anywhere: the default hour applies.
		{"UTC", "1705320000", "1705316400", "no neighbour -> default hour"},
		// A neighbour exists at the same offset (DST abolished by keeping the summer
		// offset), so the delta is zero and isdst does nothing.
		{"Africa/Windhoek", "1705312800", "1705312800", "equal-offset neighbour -> delta 0"},
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
			plain := runOne(t, `return tostring(os.time{year=2024,month=1,day=15,hour=12,min=0,sec=0})`).Str()
			dst := runOne(t, `return tostring(os.time{year=2024,month=1,day=15,hour=12,min=0,sec=0,isdst=true})`).Str()
			if plain != tc.plainWant || dst != tc.dstWant {
				t.Errorf("%s (%s): plain=%s want %s, isdst=true=%s want %s",
					tc.zone, tc.why, plain, tc.plainWant, dst, tc.dstWant)
			}
		}()
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
	requireGlibcMktime(t)
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
	requireGlibcMktime(t)
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
	requireGlibcMktime(t)
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

// TestOSTimeIsDST_PortableContract asserts what holds on EVERY platform, and does not skip.
//
// The isdst search uses glibc's rules regardless of host libc, which is a deliberate choice
// recorded in corners_test.go::exemptions: the same script gives the same answer everywhere,
// at the cost of deviating from a locally built PUC on non-glibc hosts for a few
// transition-adjacent inputs. The properties below follow from the rules themselves rather
// than from any libc's arithmetic, so they must hold on Linux and macOS alike -- which is
// what makes this the enforcer for the contract, rather than the Linux-only cases above.
func TestOSTimeIsDST_PortableContract(t *testing.T) {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	origLocal := time.Local
	t.Setenv("TZ", "Europe/London")
	time.Local = loc
	t.Cleanup(func() { time.Local = origLocal })

	// A two-state zone: the two isdst values must differ by exactly the zone's DST delta,
	// and the summer/winter defaults must each agree with one of them.
	sumT := runOne(t, `return tostring(os.time{year=2024,month=7,day=1,hour=12,isdst=true})`).Str()
	sumF := runOne(t, `return tostring(os.time{year=2024,month=7,day=1,hour=12,isdst=false})`).Str()
	sumD := runOne(t, `return tostring(os.time{year=2024,month=7,day=1,hour=12})`).Str()
	winT := runOne(t, `return tostring(os.time{year=2024,month=1,day=1,hour=12,isdst=true})`).Str()
	winF := runOne(t, `return tostring(os.time{year=2024,month=1,day=1,hour=12,isdst=false})`).Str()
	winD := runOne(t, `return tostring(os.time{year=2024,month=1,day=1,hour=12})`).Str()

	diff := func(a, b string) int64 {
		x, errA := strconv.ParseInt(a, 10, 64)
		y, errB := strconv.ParseInt(b, 10, 64)
		if errA != nil || errB != nil {
			t.Fatalf("os.time returned non-integers: %q, %q", a, b)
		}
		return y - x
	}
	if d := diff(sumT, sumF); d != 3600 {
		t.Errorf("summer isdst true->false delta = %d, want 3600", d)
	}
	if d := diff(winT, winF); d != 3600 {
		t.Errorf("winter isdst true->false delta = %d, want 3600", d)
	}
	// In summer the zone is in DST, so the default matches isdst=true; in winter, false.
	if sumD != sumT {
		t.Errorf("summer default %s should match isdst=true %s", sumD, sumT)
	}
	if winD != winF {
		t.Errorf("winter default %s should match isdst=false %s", winD, winF)
	}
	// isdst must be idempotent: asking twice gives the same answer.
	if again := runOne(t, `return tostring(os.time{year=2024,month=7,day=1,hour=12,isdst=true})`).Str(); again != sumT {
		t.Errorf("not idempotent: %s then %s", sumT, again)
	}
}
