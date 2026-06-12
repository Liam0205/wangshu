// Random script generator for differential fuzzing (12 §3.2)。
//
// 受控文法生成确定性脚本(同 seed 同输出):只产"可安全对拍"的构造——
// 数值算术/字符串拼接/比较/局部变量/控制流/函数调用。避开实现定义行为
// (pairs 序、tostring(table) 地址等),保证输出可逐字节对拍。
package difftest

import (
	"fmt"
	"math/rand"
	"strings"
)

type localKind uint8

const (
	localNum localKind = iota
	localStr
	localTbl
)

type genState struct {
	rng    *rand.Rand
	sb     strings.Builder
	locals []localKind
	depth  int
}

// generateScript 产一个以 `return <expr>` 结尾的确定性脚本。
func generateScript(seed int64) string {
	g := &genState{rng: rand.New(rand.NewSource(seed))}
	n := 3 + g.rng.Intn(5)
	for i := 0; i < n; i++ {
		g.stmt()
	}
	// 终结:return 全部局部的 tostring 拼接(输出依赖全部路径)。
	// 表局部取 #t(长度可对拍;内容序不可对拍)。
	g.sb.WriteString("return ")
	if len(g.locals) == 0 {
		g.sb.WriteString(g.numExpr())
	} else {
		parts := make([]string, 0, len(g.locals))
		for i, k := range g.locals {
			if k == localTbl {
				parts = append(parts, fmt.Sprintf("tostring(#v%d)", i))
			} else {
				parts = append(parts, fmt.Sprintf("tostring(v%d)", i))
			}
		}
		g.sb.WriteString(strings.Join(parts, " .. \"|\" .. "))
	}
	g.sb.WriteString("\n")
	return g.sb.String()
}

func (g *genState) stmt() {
	switch g.rng.Intn(10) {
	case 0:
		g.declNum()
	case 1:
		g.declStr()
	case 2:
		g.forLoop()
	case 3:
		g.ifStmt()
	case 4:
		g.funcDef()
	case 5:
		g.declTbl()
	case 6:
		g.whileLoop()
	case 7:
		g.repeatLoop()
	case 8:
		g.closureDef()
	case 9:
		g.multiAssign()
	}
}

func (g *genState) newLocal(k localKind) string {
	name := fmt.Sprintf("v%d", len(g.locals))
	g.locals = append(g.locals, k)
	return name
}

// pickLocal 找一个指定类型的局部;没有则新建。
func (g *genState) pickLocal(k localKind) string {
	var cand []int
	for i, lk := range g.locals {
		if lk == k {
			cand = append(cand, i)
		}
	}
	if len(cand) == 0 {
		switch k {
		case localNum:
			g.declNum()
		case localStr:
			g.declStr()
		case localTbl:
			g.declTbl()
		}
		return fmt.Sprintf("v%d", len(g.locals)-1)
	}
	return fmt.Sprintf("v%d", cand[g.rng.Intn(len(cand))])
}

func (g *genState) declNum() {
	name := g.newLocal(localNum)
	fmt.Fprintf(&g.sb, "local %s = %s\n", name, g.numExpr())
}

func (g *genState) declStr() {
	name := g.newLocal(localStr)
	fmt.Fprintf(&g.sb, "local %s = %s\n", name, g.strExpr())
}

// declTbl 声明一个数组表局部 + 若干写操作。
func (g *genState) declTbl() {
	name := g.newLocal(localTbl)
	n := 1 + g.rng.Intn(4)
	elems := make([]string, n)
	for i := range elems {
		elems[i] = g.numExpr()
	}
	fmt.Fprintf(&g.sb, "local %s = { %s }\n", name, strings.Join(elems, ", "))
	// 随机追加写/读
	switch g.rng.Intn(3) {
	case 0:
		fmt.Fprintf(&g.sb, "%s[#%s + 1] = %s\n", name, name, g.numExpr())
	case 1:
		fmt.Fprintf(&g.sb, "%s[1] = %s\n", name, g.numExpr())
	case 2:
		acc := g.pickLocal(localNum)
		fmt.Fprintf(&g.sb, "%s = %s + (%s[1] or 0)\n", acc, acc, name)
	}
}

func (g *genState) forLoop() {
	acc := g.pickLocal(localNum)
	lo := 1 + g.rng.Intn(3)
	hi := lo + g.rng.Intn(10)
	step := ""
	if g.rng.Intn(3) == 0 {
		step = fmt.Sprintf(", %d", 1+g.rng.Intn(3))
	}
	fmt.Fprintf(&g.sb, "for i = %d, %d%s do %s = %s + i end\n", lo, hi, step, acc, acc)
}

func (g *genState) whileLoop() {
	cnt := g.newLocal(localNum)
	acc := g.pickLocal(localNum)
	limit := 1 + g.rng.Intn(8)
	fmt.Fprintf(&g.sb, "local %s = 0\nwhile %s < %d do %s = %s + 1; %s = %s + %s end\n",
		cnt, cnt, limit, cnt, cnt, acc, acc, cnt)
}

func (g *genState) repeatLoop() {
	cnt := g.newLocal(localNum)
	limit := 1 + g.rng.Intn(6)
	fmt.Fprintf(&g.sb, "local %s = 0\nrepeat %s = %s + 1 until %s >= %d\n",
		cnt, cnt, cnt, cnt, limit)
}

func (g *genState) ifStmt() {
	v := g.pickLocal(localNum)
	switch g.rng.Intn(3) {
	case 0:
		fmt.Fprintf(&g.sb, "if %s > %d then %s = %s else %s = %s end\n",
			v, g.rng.Intn(100), v, g.numExpr(), v, g.numExpr())
	case 1:
		// elseif 链
		fmt.Fprintf(&g.sb, "if %s > %d then %s = %s elseif %s > %d then %s = %s else %s = %s end\n",
			v, 50+g.rng.Intn(50), v, g.numExpr(),
			v, g.rng.Intn(50), v, g.numExpr(), v, g.numExpr())
	case 2:
		// 逻辑算符条件
		w := g.pickLocal(localNum)
		fmt.Fprintf(&g.sb, "if %s > 0 and %s >= 0 or %s == %s then %s = %s + 1 end\n",
			v, w, v, w, v, v)
	}
}

func (g *genState) funcDef() {
	name := g.newLocal(localNum)
	op := []string{"+", "-", "*"}[g.rng.Intn(3)]
	fmt.Fprintf(&g.sb, "local function f%s(a, b) return a %s b end\nlocal %s = f%s(%d, %d)\n",
		name, op, name, name, g.rng.Intn(100), 1+g.rng.Intn(100))
}

// closureDef 闭包捕获 + 多次调用。
func (g *genState) closureDef() {
	name := g.newLocal(localNum)
	fmt.Fprintf(&g.sb, `local function mk%s()
  local n = %d
  return function() n = n + 1; return n end
end
local c%s = mk%s()
c%s()
local %s = c%s()
`, name, g.rng.Intn(10), name, name, name, name, name)
}

// multiAssign 多值返回 + 截断/补 nil。
func (g *genState) multiAssign() {
	a := g.newLocal(localNum)
	b := g.newLocal(localNum)
	fmt.Fprintf(&g.sb, "local function two%s() return %s, %s end\nlocal %s, %s = two%s()\n",
		a, g.numExpr(), g.numExpr(), a, b, a)
}

// numExpr 产一个安全的数值表达式。
func (g *genState) numExpr() string {
	if g.depth > 3 {
		return fmt.Sprintf("%d", g.rng.Intn(1000))
	}
	g.depth++
	defer func() { g.depth-- }()
	switch g.rng.Intn(9) {
	case 0:
		return fmt.Sprintf("%d", g.rng.Intn(1000))
	case 1:
		// 控制小数位避免 %.14g 边缘(由 seed corpus 的专项用例覆盖)
		return fmt.Sprintf("%.4f", g.rng.Float64()*100)
	case 2:
		return fmt.Sprintf("(%s + %s)", g.numExpr(), g.numExpr())
	case 3:
		return fmt.Sprintf("(%s * %s)", g.numExpr(), g.numExpr())
	case 4:
		return fmt.Sprintf("(%s - %s)", g.numExpr(), g.numExpr())
	case 5:
		return fmt.Sprintf("math.floor(%s)", g.numExpr())
	case 6:
		return fmt.Sprintf("math.max(%s, %s)", g.numExpr(), g.numExpr())
	case 7:
		return fmt.Sprintf("(%s %% %d)", g.numExpr(), 1+g.rng.Intn(9)) // mod 非零
	case 8:
		return fmt.Sprintf("#%s", g.strExpr()) // 字符串长度
	}
	return "0"
}

// strExpr 产一个安全的字符串表达式。
func (g *genState) strExpr() string {
	words := []string{`"abc"`, `"hello"`, `"x"`, `""`, `"42"`}
	if g.depth > 2 {
		return words[g.rng.Intn(len(words))]
	}
	g.depth++
	defer func() { g.depth-- }()
	switch g.rng.Intn(7) {
	case 0, 1:
		return words[g.rng.Intn(len(words))]
	case 2:
		return fmt.Sprintf("(%s .. %s)", g.strExpr(), g.strExpr())
	case 3:
		return fmt.Sprintf("string.upper(%s)", g.strExpr())
	case 4:
		return fmt.Sprintf("string.sub(%s, %d)", g.strExpr(), 1+g.rng.Intn(3))
	case 5:
		return fmt.Sprintf("string.rep(%s, %d)", g.strExpr(), g.rng.Intn(4))
	case 6:
		return fmt.Sprintf("string.format(\"%%d\", %d)", g.rng.Intn(1000))
	}
	return `""`
}
