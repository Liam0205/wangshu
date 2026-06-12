// Package luasuite 跑官方 Lua 5.1.5 测试套(lua.org/tests)的可运行子集。
//
// 官方测试套是语言作者写的语义断言,权威性高于自写 probe。testdata/ 内
// 13 个文件原样拷自 lua-5.1-tests(README:"main goal is to try to crash
// Lua"),不做任何修改;无法整文件通过的,在 stopAt 表登记**豁免线**
// (该行起依赖豁免特性:setfenv/getfenv、debug.*、io.tmpfile、
// os.setlocale、string.dump、require——均在 corners_test.go 豁免注册表),
// 截断到豁免线前执行,前缀部分必须全过。
//
// 未列 stopAt 的文件必须整文件通过。豁免线只许前移(实现更多特性后),
// 不许后退。
package luasuite

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Liam0205/wangshu"
)

// stopAt:文件 → 豁免线(1-based 行号,执行 [1, stopAt) 行)。
// 0 = 整文件运行。理由必须指向豁免注册表中的条目。
var stopAt = map[string]int{
	// 整文件通过
	"vararg.lua": 0,
	"sort.lua":   0,
	"pm.lua":     0,

	// getmetatable(io.stdin):io 对象模型未实现(io 豁免面)
	"errors.lua": 110,
	// debug.getinfo / string.dump / require(豁免:debug 高级面 / string.dump / require)
	"calls.lua": 165,
	// setfenv(events.lua:5 起全文件依赖;豁免:getfenv/setfenv)
	"events.lua": 4,
	// debug 库(constructs.lua:189)
	"constructs.lua": 187,
	// 行尾测试段内嵌 prog 依赖 debug.getinfo(literals.lua:112;豁免:debug)
	"literals.lua": 98,
	// getfenv/setfenv(locals.lua:58 起的 globals 测试段;豁免:getfenv/setfenv)
	"locals.lua": 54,
	// io.tmpfile(math.lua:124)
	"math.lua": 122,
	// setfenv(nextvar.lua:238 起;豁免:getfenv/setfenv)
	"nextvar.lua": 233,
	// os.setlocale(strings.lua:150)
	"strings.lua": 147,
	// setfenv(closure.lua:165)
	"closure.lua": 163,
}

func TestOfficialSuite(t *testing.T) {
	files, err := filepath.Glob("testdata/*.lua")
	if err != nil || len(files) == 0 {
		t.Fatalf("no testdata files: %v", err)
	}
	for _, f := range files {
		name := filepath.Base(f)
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			limit, known := stopAt[name]
			if !known {
				t.Fatalf("%s not registered in stopAt table (add 0 for full run or an exemption line)", name)
			}
			body := string(src)
			if limit > 0 {
				lines := strings.Split(body, "\n")
				if limit > len(lines) {
					limit = len(lines)
				}
				body = strings.Join(lines[:limit-1], "\n")
			}
			runWithTimeout(t, name, body)
		})
	}
}

func runWithTimeout(t *testing.T, name, src string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		prog, err := wangshu.Compile([]byte(src), "@"+name)
		if err != nil {
			done <- err
			return
		}
		st := wangshu.NewState(wangshu.Options{})
		_, err = prog.Run(st)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			msg := err.Error()
			if i := strings.IndexByte(msg, '\n'); i >= 0 {
				msg = msg[:i]
			}
			t.Errorf("%s: %s", name, msg)
		}
	case <-time.After(120 * time.Second):
		t.Fatalf("%s: timeout (likely interpreter hang)", name)
	}
}
