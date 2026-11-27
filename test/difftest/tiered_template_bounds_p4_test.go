//go:build wangshu_p4 && wangshu_profile

package difftest

// shorterTableCasesPromoteNaturally: on P4 a function called a few thousand times is compiled
// under the default thresholds, so TestTiered_Template_DefaultThresholds requires
// that it was entered as compiled code.
const shorterTableCasesPromoteNaturally = true
