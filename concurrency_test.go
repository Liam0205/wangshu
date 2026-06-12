// Concurrency tests — Program 跨 goroutine 共享承诺(11 §1.4 / §8)。
//
// Program 不可变、可被多 State 并发复用;State 含可变状态,每 goroutine 一个。
// LoadProgram 对每个 Proto 做 State 私有浅拷贝(Consts intern 进私有 arena、
// IC 私有、Protos 重定位),共享的只读底层(Code/StringLits/LineInfo)并发读。
// 本文件全部用例须在 `go test -race` 下通过(engineering.md -race 硬门禁)。
package wangshu_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestConcurrent_SharedProgram N 个 goroutine 各持独立 State 跑同一 Program。
func TestConcurrent_SharedProgram(t *testing.T) {
	src := `
local n = ...
local acc = 0
for i = 1, 100 do
  local t = { v = i * n }
  local f = function() return t.v end
  acc = acc + f() + #("s" .. i)
end
return acc`
	prog, err := wangshu.Compile([]byte(src), "shared")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	const goroutines = 16
	const rounds = 50
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			st := wangshu.NewState(wangshu.Options{})
			want := 0.0
			for i := 1; i <= 100; i++ {
				want += float64(i*(id+1)) + float64(len(fmt.Sprintf("s%d", i)))
			}
			for r := 0; r < rounds; r++ {
				results, err := prog.Run(st, wangshu.Number(float64(id+1)))
				if err != nil {
					errs <- fmt.Errorf("goroutine %d round %d: %v", id, r, err)
					return
				}
				if len(results) != 1 || results[0].Number() != want {
					errs <- fmt.Errorf("goroutine %d round %d: got %s want %v",
						id, r, results[0].Display(), want)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// TestConcurrent_MultiProgramPerState 多 Program 在多 State 上交叉并发。
func TestConcurrent_MultiProgramPerState(t *testing.T) {
	progs := make([]*wangshu.Program, 4)
	for i := range progs {
		src := fmt.Sprintf(`
local t = {}
for k = 1, 50 do t[k] = k * %d end
local s = 0
for _, v in ipairs(t) do s = s + v end
return s`, i+1)
		p, err := wangshu.Compile([]byte(src), fmt.Sprintf("p%d", i))
		if err != nil {
			t.Fatalf("compile %d: %v", i, err)
		}
		progs[i] = p
	}
	wants := make([]float64, 4)
	for i := range wants {
		wants[i] = float64(50 * 51 / 2 * (i + 1))
	}

	const goroutines = 8
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			st := wangshu.NewState(wangshu.Options{})
			for r := 0; r < 30; r++ {
				for pi, p := range progs {
					results, err := p.Run(st)
					if err != nil {
						errs <- fmt.Errorf("goroutine %d prog %d: %v", id, pi, err)
						return
					}
					if results[0].Number() != wants[pi] {
						errs <- fmt.Errorf("goroutine %d prog %d: got %s want %v",
							id, pi, results[0].Display(), wants[pi])
						return
					}
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// TestConcurrent_GCIndependence 各 State 的 GC 完全独立:一个 State 高压
// GC(stress mode)不得影响其它 State 的结果。
func TestConcurrent_GCIndependence(t *testing.T) {
	src := `
local keep = {}
for i = 1, 60 do
  keep[#keep + 1] = { id = i, name = "obj" .. i }
  if i % 3 == 0 then keep[#keep] = nil end
end
local count, sum = 0, 0
for _, o in pairs(keep) do count = count + 1; sum = sum + o.id end
return count, sum`
	prog, err := wangshu.Compile([]byte(src), "gcind")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	const goroutines = 8
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			st := wangshu.NewState(wangshu.Options{})
			st.SetGCStressMode(id%2 == 0) // 半数 State 全程压力 GC
			var first string
			for r := 0; r < 20; r++ {
				results, err := prog.Run(st)
				if err != nil {
					errs <- fmt.Errorf("goroutine %d round %d: %v", id, r, err)
					return
				}
				got := results[0].Display() + "|" + results[1].Display()
				if r == 0 {
					first = got
				} else if got != first {
					errs <- fmt.Errorf("goroutine %d round %d: %s != first %s", id, r, got, first)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}
