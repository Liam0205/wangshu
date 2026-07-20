//go:build !race && (wangshu_p3 || wangshu_p4) && wangshu_profile

package regression

// raceEnabled is false when the test binary was built without -race.
// P4 mmap-segment shim calls to Go helpers are known to be incompatible
// with the race detector's stack unwinder (mmap+morestack); tests that
// exercise the shim path skip themselves under -race.
const raceEnabled = false
