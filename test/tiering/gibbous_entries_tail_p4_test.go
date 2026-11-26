//go:build wangshu_p4 && !wangshu_p3 && wangshu_profile

package tiering_test

// tailCallEntersCompiled reports whether the interpreter's TAILCALL hands a promoted callee to
// compiled code on this tier. P4 does, when the callee has native code.
const tailCallEntersCompiled = true
