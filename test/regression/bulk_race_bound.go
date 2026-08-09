// bulkRaceBuild reports whether this is a -race build, for the one test that still scales a wall-clock
// bound by it (issue224_watchdog_margin_test.go, which is behind wangshu_p4).
//
// Tagged wangshu_p4 to match that sole consumer: issue222's bound was removed once it was shown to
// measure the runner rather than the charge, leaving this constant unused in the default build, which
// golangci-lint correctly reported.
//go:build !race && wangshu_p4

package regression

const bulkRaceBuild = false
