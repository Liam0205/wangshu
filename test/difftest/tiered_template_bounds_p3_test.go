//go:build wangshu_p3 && !wangshu_p4 && wangshu_profile

package difftest

// shorterTableCasesPromoteNaturally: P3 has no single-access template; the cases only have to
// agree with the interpreter there, whether or not the function is promoted.
const shorterTableCasesPromoteNaturally = false
