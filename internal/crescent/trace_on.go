//go:build wangshu_trace

package crescent

const traceExec = true

// ciMirrorCheck: under the wangshu_trace build, after enterLuaFrame pushes a
// frame it reads back the ci segment and self-checks the mirror is field-for-field
// identical to the Go cis (PW10 R2b-1 safety net against packing bugs). In the
// default build it is false and compiled away.
const ciMirrorCheck = true
