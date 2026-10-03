//go:build !windows

package regression

import (
	"os"
	"path/filepath"
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestLoadfileErrorsCarryStrerror covers #288: lauxlib.c's errfile reports "cannot <what> <file>:
// <strerror>", where what is "open" when fopen fails and "read" when the file opens but reading it
// fails (a directory). loadfile and dofile reported only "cannot open <file>". It also covers the
// first-line skip luaL_loadfile does for a script starting with '#' (a "#!" line), which loadfile
// lacked, so such a script was a syntax error. A file name ends at its first NUL byte, since fopen and
// errfile's %s both read it as a C string. Expectations are lua5.1's, on Linux and macOS, whose C
// libraries give these strerror texts.
func TestLoadfileErrorsCarryStrerror(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("plain.lua", "return 1\n")
	write("shebang.lua", "#!/usr/bin/lua\nreturn debug.getinfo(1, 'l').currentline\n")
	write("shebang2.lua", "#!/usr/bin/lua\nerror('line2')\n")
	write("hashonly.lua", "#no newline")
	if err := os.Mkdir(filepath.Join(dir, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	const src = `print(loadfile("nonexist.lua"))
print(pcall(dofile, "nonexist.lua"))
print(loadfile("adir"))
print(loadfile("plain.lua/x"))
print(pcall(dofile, "shebang.lua"))
print(pcall(dofile, "shebang2.lua"))
print(pcall(dofile, "hashonly.lua"))
print(pcall(dofile, "plain.lua\0xyz"))
print(loadfile("nonexist\0xyz"))
print(pcall(dofile, "nonexist\0xyz"))
print(loadfile("\0"))
`
	const want = "nil\tcannot open nonexist.lua: No such file or directory\n" +
		"false\tcannot open nonexist.lua: No such file or directory\n" +
		"nil\tcannot read adir: Is a directory\n" +
		"nil\tcannot open plain.lua/x: Not a directory\n" +
		"true\t2\n" +
		"false\tshebang2.lua:2: line2\n" +
		"true\n" +
		"true\t1\n" +
		"nil\tcannot open nonexist: No such file or directory\n" +
		"false\tcannot open nonexist: No such file or directory\n" +
		"nil\tcannot open : No such file or directory\n"
	if got := printedBy(t, wangshu.Options{AllowFileLoad: true}, src); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}
