// bulkRaceBuild reports whether this is a -race build, for the one test that still scales a wall-clock
// bound by it (issue224_watchdog_margin_test.go, which is behind wangshu_p4).
//
// Tagged wangshu_p4 to match that sole consumer: issue222's bound was removed once it was shown to
// measure the runner rather than the charge, leaving this constant unused in the default build, which
// golangci-lint correctly reported.
//go:build race && wangshu_p4

package regression

// bulkRaceBuild reports whether this binary has the race detector enabled.
//
// A distinct name from raceEnabled in race_on_test.go / race_off_test.go: that pair is scoped to
// (wangshu_p3 || wangshu_p4) && wangshu_profile builds, so reusing it collided there while leaving
// the default build without a definition. Declared for every build instead.
const bulkRaceBuild = true
