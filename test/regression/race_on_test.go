//go:build race && (wangshu_p3 || wangshu_p4) && wangshu_profile

package regression

// raceEnabled is true when the test binary was built with -race. See
// race_off_test.go for the rationale (shim-mmap+morestack conflict).
const raceEnabled = true
