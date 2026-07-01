//go:build wangshu_p4 && amd64 && linux && !race

package peroptranslator

// raceEnabled is false when the test binary was built without -race.
const raceEnabled = false
