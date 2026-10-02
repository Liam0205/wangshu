//go:build wangshu_oracle_cgo && cgo && !(wangshu_p3 || wangshu_p4)

package fuzz_test

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/oracle"
)

// tieredBuild reports whether runTieredSide exists in this build (it needs a compiled tier).
const tieredBuild = false

func runTieredSide(t *testing.T, src, prelude string) (oracle.Verdict, string, string, bool) {
	t.Helper()
	t.Fatal("runTieredSide called in a build without a compiled tier")
	return 0, "", "", false
}
