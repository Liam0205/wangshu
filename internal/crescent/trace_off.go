//go:build !wangshu_trace

package crescent

// traceExec is the compile-time switch for per-instruction trace: default
// false, the main loop's trace branch is entirely eliminated by the compiler
// (zero overhead, and not a global variable, so no concurrent data race
// surface). For troubleshooting, enable with `go build -tags wangshu_trace`.
const traceExec = false

// ciMirrorCheck default false: the ci segment mirror self-check from
// PW10 R2b-1 is compiled away in the default build (zero overhead).
// `go build -tags wangshu_trace` enables the read-back self-check.
const ciMirrorCheck = false
