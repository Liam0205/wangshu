//go:build race

package regression

// raceEnabled reports whether the binary was built with -race, whose memory instrumentation makes
// wall-clock bounds in this package about twice as loose.
const raceEnabled = true
