//go:build wangshu_oracle_cgo && cgo && (wangshu_p3 || wangshu_p4)

package fuzz_test

// tieredBuild reports whether runTieredSide exists in this build (it needs a compiled tier).
const tieredBuild = true
