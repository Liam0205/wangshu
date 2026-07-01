//go:build wangshu_p4 && amd64 && linux && race

package peroptranslator

// raceEnabled is true when the test binary was built with -race.
// Shim-based e2e tests trip the mmap+morestack incompatibility under
// -race and must be skipped in that mode. See reflection
// 2026-07-01-p4-pj10-native-round lesson 1.
const raceEnabled = true
