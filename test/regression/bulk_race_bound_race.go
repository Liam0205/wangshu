//go:build race

package regression

// bulkRaceBuild reports whether this binary has the race detector enabled.
//
// A distinct name from raceEnabled in race_on_test.go / race_off_test.go: that pair is scoped to
// (wangshu_p3 || wangshu_p4) && wangshu_profile builds, so reusing it collided there while leaving
// the default build without a definition. Declared for every build instead.
const bulkRaceBuild = true
