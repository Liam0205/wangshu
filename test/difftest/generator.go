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
	switch g.rng.Intn(19) {
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
	case 10:
		g.genericFor()
	case 11:
		g.nestedFunc()
	case 12:
		g.patternStmt()
	case 13:
		g.metaIndex()
	case 14:
		g.coroutineStmt()
	case 15:
		g.metaOperators()
	case 16:
		g.metaCallable()
	case 17:
		g.loadstringStmt()
	case 18:
		g.tostringMeta()
	}
}

func (g *genState) newLocal(k localKind) string {
	name := fmt.Sprintf("v%d", len(g.locals))
	g.locals = append(g.locals, k)
	return name
}

// pickLocal 找一个指定类型的局部;没有则新建。
//
// 新建路径必须按"声明语句产生的局部下标"回查(decl* 内部可能级联新建其它
// 类型局部,len-1 不一定是目标类型)。
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
		// 重新扫描:取新建后第一个该类型局部
		for i, lk := range g.locals {
			if lk == k {
				return fmt.Sprintf("v%d", i)
			}
		}
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

// genericFor 泛型 for:ipairs 序确定可直接对拍;pairs 用序无关聚合(求和/计数)。
func (g *genState) genericFor() {
	t := g.pickLocal(localTbl)
	acc := g.pickLocal(localNum)
	if g.rng.Intn(2) == 0 {
		// ipairs:序确定
		fmt.Fprintf(&g.sb, "for i, x in ipairs(%s) do %s = %s + i * (x %% 97) end\n", t, acc, acc)
	} else {
		// pairs:只做序无关聚合(键计数 + 数值求和)
		fmt.Fprintf(&g.sb, "for k, x in pairs(%s) do %s = %s + 1 + (type(x) == \"number\" and (x %% 89) or 0) end\n",
			t, acc, acc)
	}
}

// nestedFunc 两层嵌套函数定义(内层捕获外层形参)。
func (g *genState) nestedFunc() {
	name := g.newLocal(localNum)
	fmt.Fprintf(&g.sb, `local function outer%s(a)
  local function inner(b) return a + b end
  return inner(%s)
end
local %s = outer%s(%d)
`, name, g.numExpr(), name, name, g.rng.Intn(100))
}

// patternStmt 受控模式集的 string.find/match/gsub(模式固定白名单,主语随机)。
func (g *genState) patternStmt() {
	name := g.newLocal(localStr)
	subjects := []string{
		`"abc 123 def"`, `"key=value;x=y"`, `"  spaced  "`, `"UPPER lower"`,
		`"a1b2c3"`, `"hello-world"`, `"[bracket]"`,
	}
	subj := subjects[g.rng.Intn(len(subjects))]
	switch g.rng.Intn(4) {
	case 0:
		pats := []string{`"%d+"`, `"%a+"`, `"%w+"`, `"[a-z]+"`}
		fmt.Fprintf(&g.sb, "local %s = tostring(string.match(%s, %s))\n",
			name, subj, pats[g.rng.Intn(len(pats))])
	case 1:
		pats := []string{`"%s"`, `"="`, `"%d"`}
		fmt.Fprintf(&g.sb, "local %s = tostring(string.find(%s, %s))\n",
			name, subj, pats[g.rng.Intn(len(pats))])
	case 2:
		fmt.Fprintf(&g.sb, "local %s = string.gsub(%s, \"%%a\", \"_\")\n", name, subj)
	case 3:
		fmt.Fprintf(&g.sb, `local %s = ""
for w in string.gmatch(%s, "%%w+") do %s = %s .. w .. "," end
`, name, subj, name, name)
	}
}

// metaIndex 元表 __index 两形态(表链 / 函数)。
func (g *genState) metaIndex() {
	name := g.newLocal(localNum)
	if g.rng.Intn(2) == 0 {
		fmt.Fprintf(&g.sb, `local base%s = { v = %d }
local t%s = setmetatable({}, { __index = base%s })
local %s = t%s.v
`, name, g.rng.Intn(1000), name, name, name, name)
	} else {
		fmt.Fprintf(&g.sb, `local t%s = setmetatable({}, { __index = function(_, k) return #k + %d end })
local %s = t%s.somekey
`, name, g.rng.Intn(50), name, name)
	}
}

// coroutineStmt 协程 yield 次序对拍(yield 序确定)。
func (g *genState) coroutineStmt() {
	name := g.newLocal(localStr)
	n := 2 + g.rng.Intn(3)
	fmt.Fprintf(&g.sb, `local co%s = coroutine.create(function(z)
  for i = 1, %d do z = coroutine.yield(z + i) end
  return z
end)
local %s = ""
local arg%s = %d
for i = 1, %d do
  local ok, v = coroutine.resume(co%s, arg%s)
  %s = %s .. tostring(v) .. ";"
end
`, name, n, name, name, g.rng.Intn(20), n+1, name, name, name, name)
}

// metaOperators 算术/比较元方法(__add/__lt/__le/__eq 随机一种,行为确定)。
func (g *genState) metaOperators() {
	name := g.newLocal(localStr)
	switch g.rng.Intn(4) {
	case 0:
		// __add:b 可能是裸 number,须 type 判别后取值(number 不可索引)
		fmt.Fprintf(&g.sb, `local mt%s = { __add = function(a, b)
  local av = type(a) == "table" and a.v or a
  local bv = type(b) == "table" and b.v or b
  return av + bv
end }
local o%s = setmetatable({ v = %d }, mt%s)
local %s = tostring(o%s + %d)
`, name, name, g.rng.Intn(50), name, name, name, g.rng.Intn(50))
	case 1:
		fmt.Fprintf(&g.sb, `local mt%s = { __lt = function(a, b) return a.v < b.v end }
local a%s = setmetatable({ v = %d }, mt%s)
local b%s = setmetatable({ v = %d }, mt%s)
local %s = tostring(a%s < b%s)
`, name, name, g.rng.Intn(50), name, name, g.rng.Intn(50), name, name, name, name)
	case 2:
		// __le 经 __lt 回退(5.1 特有)
		fmt.Fprintf(&g.sb, `local mt%s = { __lt = function(a, b) return a.v < b.v end }
local a%s = setmetatable({ v = %d }, mt%s)
local b%s = setmetatable({ v = %d }, mt%s)
local %s = tostring(a%s <= b%s)
`, name, name, g.rng.Intn(50), name, name, g.rng.Intn(50), name, name, name, name)
	case 3:
		fmt.Fprintf(&g.sb, `local mt%s = { __eq = function(a, b) return a.v == b.v end }
local a%s = setmetatable({ v = %d }, mt%s)
local b%s = setmetatable({ v = %d }, mt%s)
local %s = tostring(a%s == b%s)
`, name, name, g.rng.Intn(3), name, name, g.rng.Intn(3), name, name, name, name)
	}
}

// metaCallable __call 可调用对象。
func (g *genState) metaCallable() {
	name := g.newLocal(localNum)
	op := []string{"+", "*", "-"}[g.rng.Intn(3)]
	fmt.Fprintf(&g.sb, `local c%s = setmetatable({ base = %d }, { __call = function(self, x) return self.base %s x end })
local %s = c%s(%d)
`, name, g.rng.Intn(100), op, name, name, 1+g.rng.Intn(100))
}

// loadstringStmt 动态编译执行(代码串确定)。
func (g *genState) loadstringStmt() {
	name := g.newLocal(localNum)
	a, b := g.rng.Intn(100), 1+g.rng.Intn(100)
	op := []string{"+", "*", "-"}[g.rng.Intn(3)]
	fmt.Fprintf(&g.sb, "local f%s = loadstring(\"return %d %s %d\")\nlocal %s = f%s()\n",
		name, a, op, b, name, name)
}

// tostringMeta __tostring 元方法。
func (g *genState) tostringMeta() {
	name := g.newLocal(localStr)
	fmt.Fprintf(&g.sb, `local o%s = setmetatable({}, { __tostring = function() return "obj<%d>" end })
local %s = tostring(o%s)
`, name, g.rng.Intn(1000), name, name)
}

// numExpr 产一个安全的数值表达式。
func (g *genState) numExpr() string {
	if g.depth > 3 {
		return fmt.Sprintf("%d", g.rng.Intn(1000))
	}
	g.depth++
	defer func() { g.depth-- }()
	switch g.rng.Intn(10) {
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
	case 9:
		// 短路表达式嵌入算术:`(cond and K1 or K2) op K3`——带跳转链的
		// eKNum 不可折叠(isnumeral),此形态曾绕过全部防线触发 Go panic。
		cond := []string{"true", "false", "(1 < 2)", "(2 < 1)"}[g.rng.Intn(4)]
		return fmt.Sprintf("((%s and %d or %d) + %d)",
			cond, g.rng.Intn(100), g.rng.Intn(100), g.rng.Intn(100))
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
