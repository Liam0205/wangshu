//go:build wangshu_p3 && !wangshu_p4 && wangshu_profile

package tiering_test

// tailCallEntersCompiled is false on P3: the interpreter runs a tail-called frame itself.
const tailCallEntersCompiled = false
