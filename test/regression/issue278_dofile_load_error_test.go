package regression

import (
	"os"
	"path/filepath"
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestDofileLoadErrorHasNoCallerPosition covers luaB_dofile raising loadfile's message with a bare
// lua_error (#278's family: an error a C function raises itself carries no position of its
// caller). The message already starts with the file and line of a syntax error, and lua5.1 adds
// nothing in front of it, whether dofile is called from Lua code, through pcall, or left uncaught.
// dofile used to raise it as a level-1 error, so a Lua caller's line was put in front.
// Expectations are lua5.1's, run from the same directory with the script named "x".
func TestDofileLoadErrorHasNoCallerPosition(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.lx"), []byte("x = = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	const src = `local out = {}
out[#out + 1] = select(2, pcall(dofile, "bad.lx"))
out[#out + 1] = select(2, pcall(function() dofile("bad.lx") end))
out[#out + 1] = select(2, pcall(function() local f = dofile("bad.lx") return f end))
OUT = table.concat(out, "\n")
dofile("bad.lx")`
	const msg = "bad.lx:1: unexpected symbol near '='"
	st := wangshu.NewState(wangshu.Options{AllowFileLoad: true})
	prog, err := wangshu.Compile([]byte(src), "@x")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	_, runErr := prog.Run(st)
	if got, want := st.GetGlobal("OUT").Str(), msg+"\n"+msg+"\n"+msg; got != want {
		t.Errorf("caught:\n got %q\nwant %q", got, want)
	}
	const wantUncaught = msg + "\nstack traceback:\n\t[C]: in function 'dofile'\n\tx:6: in main chunk\n\t[C]: ?"
	if runErr == nil || runErr.Error() != wantUncaught {
		t.Errorf("uncaught:\n got %v\nwant %q", runErr, wantUncaught)
	}
}
