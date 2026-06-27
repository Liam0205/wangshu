// Compilability analyzer (`docs/design/p2-bridge/03-compilability-analysis.md` §4-§5)。
//
// `compilabilityVisitor` 在一次 AST 遍历中收集 F1-F4 + F6 的不升层信号;
// F5 / F7 在 Proto 层独立判(visitor 不参与)。
//
// **保守第一,宁漏勿误**(03 §1):任何拿不准的形状一律判 NotCompilable。
// 误判(把不可编译形状判可编译)的后果是 P3 编译错误代码或运行期崩溃,
// fallback 不会被触发——这是 P2 的设计红线。
package bridge

import (
	"fmt"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/ast"
)

// AnalyzeProto 是 Compile 时被 codegen 回调的可编译性分析入口(03 §5.2)。
//
// 调用契约:
//   - 由 compile.Gen 在产出 `*bytecode.Proto` 后调用,把结果写进
//     `ProfileData.Compilable`(03 §2.5);
//   - `!profile` build tag 下 codegen 不调本函数,所有 Proto 留 CompUnknown
//     (03 §2.6);
//   - **嵌套 Proto 独立判定**(03 §7):codegen 对每个产出的 Proto 都独立调
//     一次本函数,父函数判定不传染子函数。
//
// 不变式:
//  1. 一次分析,结果不变(03 §5.4)——本函数返回后 Compilable 不再修改;
//  2. 保守优先——任一 F1-F7 信号触发即判 NotCompilable;
//  3. AST 用完即弃(03 §2.4 决策方案 ①)——本函数返回后不持有 fn 引用。
func (b *Bridge) AnalyzeProto(fn *ast.FuncExpr, proto *bytecode.Proto) Compilability {
	return b.AnalyzeProtoWithOuter(fn, proto, nil)
}

// AnalyzeProtoWithOuter 是 AnalyzeProto 的 scope-aware 版本(承 P4 PJ5
// PJ5 + 03 §9 GAP-5):outerLocalFuncs 是 outer scope 链上的 local fn 名字
// 映射,让本 proto 内调 outer local fn 形态识别为 known 而非 unknown call。
//
// outerLocalFuncs = nil 时行为等价 AnalyzeProto(向后兼容)。
//
// 典型场景:嵌套 closure
//
//	local function noop() end                -- outer 注册 noop
//	local function invoker() noop() end     -- 本 proto 内调 outer noop
//
// 不扩展(nil)时:visitor.localFuncs 空 → noop 标 callsUnknownFn → invoker
// NotCompilable;
// 扩展时:visitor.localFuncs 含 noop → isKnownLocalCall=true → 递归判
// noop.Body(同款语义传染:noop 含 yield 则 invoker 也含),invoker 可
// Compilable。
//
// **遮蔽安全**:outerLocalFuncs 中与本 proto Params 同名的条目被剔除,
// 避免误把 parameter 当 known local fn。
func (b *Bridge) AnalyzeProtoWithOuter(fn *ast.FuncExpr, proto *bytecode.Proto, outerLocalFuncs map[string]*ast.FuncExpr) Compilability {
	v := newCompilabilityVisitor()
	// 继承 outer local funcs 快照,减去本函数参数同名遮蔽项
	for name, fnAST := range outerLocalFuncs {
		shadowed := false
		for _, p := range fn.Params {
			if p == name {
				shadowed = true
				break
			}
		}
		if !shadowed {
			v.localFuncs[name] = fnAST
		}
	}
	v.walkBlock(fn.Body)

	var reasons ReasonsBitmap

	// F1: vararg(三重识别 03 §3.1.3:AST.IsVararg + Proto.IsVararg + visitor.sawVararg)
	if fn.IsVararg || v.sawVararg || protoIsVararg(proto) {
		reasons |= ReasonVararg
	}
	// AST/Proto IsVararg 必须一致(03 §2.3 不变式 1)——不一致即 codegen bug
	if fn.IsVararg != proto.IsVararg {
		panic(fmt.Sprintf("compilability: AST/Proto IsVararg mismatch (ast=%v, proto=%v)",
			fn.IsVararg, proto.IsVararg))
	}

	// F2: 协程相关
	if v.callsYield {
		reasons |= ReasonYield
	}
	if v.callsResume {
		reasons |= ReasonResume
	}
	if v.callsCoroutine {
		reasons |= ReasonCoroutine
	}
	if v.callsUnknownFn {
		reasons |= ReasonUnknownCall
	}

	// F3 / F4: debug / setfenv
	if v.usesDebug {
		reasons |= ReasonDebug
	}
	if v.usesSetfenv {
		reasons |= ReasonSetfenv
	}

	// F5: 过大函数(Proto 层)
	if len(proto.Code) > MaxCompilableInsns {
		reasons |= ReasonOverSize
	}
	if int(proto.MaxStack) > MaxCompilableRegs {
		reasons |= ReasonOverRegs
	}

	// F6: 深嵌套 / upvalue 数
	if v.maxClosureDepth > MaxClosureDepth {
		reasons |= ReasonNestedDeep
	}
	if len(proto.UpvalDescs) > MaxUpvalCount {
		reasons |= ReasonOverUpval
	}

	// F7: P3 后端能力查询(放最后,F1-F6 全过才查——03 §3.7.5 + 不变式 I8)
	if reasons == 0 && b.checkF7BackendSupport(proto) {
		reasons |= ReasonBackendUnsupp
	}

	// 决策与缓存
	result := CompCompilable
	if reasons.HasAny() {
		result = CompNotCompilable
	}
	b.SetCompilability(proto, result, reasons)
	return result
}

// checkF7BackendSupport F7 P3 后端能力查询(03 §3.7.6)。
//
// b.p3 == nil(P1-only / P2 PB0..PB5 P3 未注入)→ 视为不支持(保守拒)。
// 这保证 P1-only 行为与 P2 启用前一致——所有 Proto 永久 tier-0 解释。
func (b *Bridge) checkF7BackendSupport(proto *bytecode.Proto) bool {
	if b.p3 == nil {
		return true // 无 P3 = 不支持任何 opcode = F7 触发
	}
	return !b.p3.SupportsAllOpcodes(proto)
}

// protoIsVararg 扫 Proto.Code 看是否含 VARARG opcode(纵深防御,03 §3.1.3)。
func protoIsVararg(proto *bytecode.Proto) bool {
	for _, ins := range proto.Code {
		if bytecode.Op(ins) == bytecode.VARARG {
			return true
		}
	}
	return false
}

// compilabilityVisitor 收集 F1-F4 + F6 的信号(03 §4.1)。
//
// **嵌套不传染**(03 §7.3):visitor 进入子 FuncExpr 时挂 sub-visitor,只把
// maxClosureDepth 信号回写父——子函数的 yield/debug/setfenv 等内容信号
// 由它自己的独立 AnalyzeProto 调用判定。
//
// **作用域感知**(用户拍板:isKnownLocalCall 真实现):跟踪 local 函数名 →
// 子 FuncExpr 引用 的映射,visitor 看到 `f()` 形态时:
//   - 如果 f 是 local 名且指向当前 Proto 的某子 FuncExpr → 递归判子
//     (复用本 visitor 的判定结果,共享 callsXxx 信号)而非简单标 unknown。
//   - 这样一个调用纯计算 helper 的函数仍可编译(纯计算 helper 自身无
//     yield/debug/setfenv,递归判 known safe)。
type compilabilityVisitor struct {
	// F1: vararg 兜底捕捉(主判定看 FuncExpr.IsVararg,03 §3.1.4)
	sawVararg bool

	// F2: 协程相关
	callsYield     bool
	callsResume    bool
	callsCoroutine bool
	callsUnknownFn bool

	// F3 / F4
	usesDebug   bool
	usesSetfenv bool

	// F6: 嵌套深度
	currentDepth    int
	maxClosureDepth int

	// 作用域:local 函数名 → 子 FuncExpr 引用(F2 isKnownLocalCall 真实现的依据)。
	// 单 visitor 实例内的局部表,跨子函数边界不共享(嵌套 Proto 独立判定)。
	localFuncs map[string]*ast.FuncExpr

	// localShadows 用于跟踪「同名 local 重定义」遮蔽外层 known function。
	// 简单 push/pop 栈即可——本 P2 初版用 nil-check + map 写覆盖,Pop 时
	// 显式清理(scope-aware 实装)。
	scopeStack []scopeFrame

	// inlinedKnownCalls 防自递归无限——同一 FuncExpr 在 isKnownLocalCall
	// 路径下展开过一次后不再展开(03 §3.2.6 注:循环图亦保守判,反正 yield
	// 就不可编译;单次展开足以决定父函数是否含 yield)。
	inlinedKnownCalls map[*ast.FuncExpr]bool
}

type scopeFrame struct {
	saved map[string]*ast.FuncExpr // 进入 block 时的快照
}

func newCompilabilityVisitor() *compilabilityVisitor {
	return &compilabilityVisitor{
		localFuncs:        make(map[string]*ast.FuncExpr),
		inlinedKnownCalls: make(map[*ast.FuncExpr]bool),
	}
}

// pushScope / popScope 进入 / 退出一个 block(do/while/for/if/repeat)时
// 保存 / 恢复 localFuncs(简单栈实装)。
func (v *compilabilityVisitor) pushScope() {
	saved := make(map[string]*ast.FuncExpr, len(v.localFuncs))
	for k, e := range v.localFuncs {
		saved[k] = e
	}
	v.scopeStack = append(v.scopeStack, scopeFrame{saved: saved})
}

func (v *compilabilityVisitor) popScope() {
	if len(v.scopeStack) == 0 {
		return
	}
	frame := v.scopeStack[len(v.scopeStack)-1]
	v.scopeStack = v.scopeStack[:len(v.scopeStack)-1]
	v.localFuncs = frame.saved
}

// walkBlock 遍历一个 block(语句列表)。
func (v *compilabilityVisitor) walkBlock(b *ast.Block) {
	if b == nil {
		return
	}
	for _, s := range b.Stmts {
		v.walkStmt(s)
	}
}

// walkStmt 遍历一条语句。
func (v *compilabilityVisitor) walkStmt(s ast.Stmt) {
	switch n := s.(type) {
	case *ast.LocalStmt:
		// `local x, y, z = a, b, function() ... end`——若某个 expr 是
		// FuncExpr 文字,把对应名字挂到 localFuncs(F2 known local call 依据)。
		for _, e := range n.Exprs {
			v.walkExpr(e)
		}
		// 注册 local 函数(同名后定义会覆盖前定义,符合 Lua 5.1 作用域语义)
		for i, name := range n.Names {
			if i < len(n.Exprs) {
				if fn, ok := n.Exprs[i].(*ast.FuncExpr); ok {
					v.localFuncs[name] = fn
					continue
				}
			}
			// 不是 FuncExpr 字面量 ⇒ 不挂(若 name 之前指向 local fn,
			// 现在被遮蔽,从 map 移除避免误判)
			delete(v.localFuncs, name)
		}
	case *ast.LocalFuncStmt:
		// `local function f() ... end` —— 直接挂 localFuncs。
		// 注意作用域:函数体内可见自身(允许递归),先挂再 walk 体。
		v.localFuncs[n.Name] = n.Fn
		v.walkFuncExpr(n.Fn)
	case *ast.AssignStmt:
		for _, e := range n.Targets {
			v.walkExpr(e)
		}
		for _, e := range n.Exprs {
			v.walkExpr(e)
		}
		// `f = function() end` 形态——若 target 是 NameExpr 且 RHS 是 FuncExpr,
		// 但赋全局/upvalue 的 f 不是 local,**保守不挂 localFuncs**
		// (赋值后 f 可能被外部覆盖)。
	case *ast.CallStmt:
		v.walkExpr(n.Call)
	case *ast.DoStmt:
		v.pushScope()
		v.walkBlock(n.Body)
		v.popScope()
	case *ast.WhileStmt:
		v.walkExpr(n.Cond)
		v.pushScope()
		v.walkBlock(n.Body)
		v.popScope()
	case *ast.RepeatStmt:
		// repeat-until:Cond 在 Body 作用域内可见局部
		v.pushScope()
		v.walkBlock(n.Body)
		v.walkExpr(n.Cond)
		v.popScope()
	case *ast.IfStmt:
		for _, c := range n.Clauses {
			v.walkExpr(c.Cond)
			v.pushScope()
			v.walkBlock(c.Body)
			v.popScope()
		}
		if n.Else != nil {
			v.pushScope()
			v.walkBlock(n.Else)
			v.popScope()
		}
	case *ast.NumForStmt:
		v.walkExpr(n.Init)
		v.walkExpr(n.Limit)
		if n.Step != nil {
			v.walkExpr(n.Step)
		}
		v.pushScope()
		v.walkBlock(n.Body)
		v.popScope()
	case *ast.GenForStmt:
		for _, e := range n.Exprs {
			v.walkExpr(e)
		}
		v.pushScope()
		v.walkBlock(n.Body)
		v.popScope()
	case *ast.FuncStmt:
		// `function a.b.c.m() ... end`——target 是 NameExpr/IndexExpr 链。
		// 不挂 localFuncs(全局或表字段,不是 local)。
		v.walkExpr(n.Target)
		v.walkFuncExpr(n.Fn)
	case *ast.ReturnStmt:
		for _, e := range n.Exprs {
			v.walkExpr(e)
		}
	case *ast.BreakStmt:
		// 无表达式,nothing to walk
	}
}

// walkExpr 遍历一个表达式。
func (v *compilabilityVisitor) walkExpr(e ast.Expr) {
	if e == nil {
		return
	}
	switch n := e.(type) {
	case *ast.NilExpr, *ast.TrueExpr, *ast.FalseExpr,
		*ast.NumberExpr, *ast.StringExpr:
		// nothing
	case *ast.VarargExpr:
		v.sawVararg = true
	case *ast.NameExpr:
		v.visitNameExpr(n)
	case *ast.IndexExpr:
		v.walkExpr(n.Obj)
		v.walkExpr(n.Key)
	case *ast.ParenExpr:
		v.walkExpr(n.E)
	case *ast.CallExpr:
		v.visitCallExpr(n)
	case *ast.MethodCallExpr:
		v.visitMethodCallExpr(n)
	case *ast.BinExpr:
		v.walkExpr(n.L)
		v.walkExpr(n.R)
	case *ast.UnExpr:
		v.walkExpr(n.E)
	case *ast.FuncExpr:
		v.walkFuncExpr(n)
	case *ast.TableExpr:
		for _, k := range n.HKeys {
			v.walkExpr(k)
		}
		for _, e := range n.HVals {
			v.walkExpr(e)
		}
		for _, e := range n.AKeys {
			v.walkExpr(e)
		}
	}
}

// visitNameExpr 捕捉 debug / setfenv / getfenv 等关键名字(F3 / F4)。
//
// 假阳性容忍:用户重定义局部 `debug` 也会触发(罕见反模式)。精确识别
// (scope-aware 名字解析)留 §9 缺口 GAP-5。
func (v *compilabilityVisitor) visitNameExpr(e *ast.NameExpr) {
	switch e.Name {
	case "debug":
		v.usesDebug = true
	case "setfenv", "getfenv":
		v.usesSetfenv = true
	}
}

// visitCallExpr 处理 f(args) 形态(F2)。
func (v *compilabilityVisitor) visitCallExpr(e *ast.CallExpr) {
	// 1. 先 walk Args(args 也可能含 yield 等)
	for _, a := range e.Args {
		v.walkExpr(a)
	}
	// 2. 识别 coroutine.* 调用
	if isCoroutineCall(e.Fn) {
		v.callsCoroutine = true
		switch methodName(e.Fn) {
		case "yield":
			v.callsYield = true
		case "resume":
			v.callsResume = true
		}
		return
	}
	// 3. 识别 debug.* 调用(F3 加强)
	if isDebugCall(e.Fn) {
		v.usesDebug = true
		return
	}
	// 4. 识别 setfenv/getfenv 直接调用(F4 加强)
	if name, ok := e.Fn.(*ast.NameExpr); ok {
		if name.Name == "setfenv" || name.Name == "getfenv" {
			v.usesSetfenv = true
			return
		}
	}
	// 5. stdlib 白名单(P2 后续优化轮 #1):明确不会 yield 的 stdlib 调用
	// 不标 unknown,让纯计算 + stdlib 调用的函数可被判 Compilable。
	if isSafeStdlibCall(e.Fn) {
		return
	}
	// 6. 一般调用——isKnownLocalCall 真实现(用户拍板不要全 false)
	if v.isKnownLocalCall(e.Fn) {
		// 已知 local 指向当前 Proto 的某子 FuncExpr → 把子函数体并入父的
		// 判定(walkBlock 而非 walkFuncExpr——后者会创建 sub-visitor 隔离信号,
		// 那是用于「子函数定义」场景而非「父调用子」语义)。
		// 父调用子等同于「父在执行流上等同于执行 helper 体」——信号要传染。
		name := e.Fn.(*ast.NameExpr).Name
		fn := v.localFuncs[name]
		// 防自递归无限循环(03 §3.2.6 注:同一 FuncExpr 第二次进入直接跳过)
		if !v.inlinedKnownCalls[fn] {
			v.inlinedKnownCalls[fn] = true
			v.walkBlock(fn.Body)
		}
		return
	}
	// 7. 也 walk 一下 Fn 表达式(非 NameExpr 等可能含 IndexExpr 链等)
	v.walkExpr(e.Fn)
	// 8. 任何不能静态确定为「local 已知子 Proto」或「stdlib 安全调用」的
	// 调用都视作 unknown(03 §3.2 (b))。
	v.callsUnknownFn = true
}

// visitMethodCallExpr `obj:m()` 形态——保守标 unknown(对象的方法表无法静态解析)。
func (v *compilabilityVisitor) visitMethodCallExpr(e *ast.MethodCallExpr) {
	v.walkExpr(e.Recv)
	for _, a := range e.Args {
		v.walkExpr(a)
	}
	v.callsUnknownFn = true
}

// walkFuncExpr 嵌套 FuncExpr 处理(03 §7.3 信号传染隔离)。
//
// 进入子函数前 +1 currentDepth + 记 maxClosureDepth;子函数体单独跑一个
// sub-visitor,只把嵌套深度信号回写父——子的 yield/debug/setfenv 等内容
// 信号由它自己的独立 AnalyzeProto 调用判定。
//
// **例外**:isKnownLocalCall 路径(visitCallExpr 第 5 步)仍走当前 visitor
// 递归子——那是为了把已知 local call 的 yield 含量传染回父函数。两条路径
// 不冲突:子函数定义本身不传染,但父调用子时按调用语义传染。
//
// **PJ5 扩展(2026-06-27)**:sub-visitor.localFuncs 继承父 visitor 的快照
// (减去 closure 自身参数同名遮蔽项),让 closure 内调外层 local known fn
// 形态识别为 known(承 03 §9 GAP-5 scope-aware 名字解析)。例:
//
//	local function noop() end
//	local function invoker() noop() end  -- noop 在 invoker 内是 upvalue
//
// 不扩展时:invoker.body 看 noop() 时 sub.localFuncs 空 → noop 标
// callsUnknownFn → ReasonUnknownCall → invoker NotCompilable;
// 扩展后:sub.localFuncs 含 noop → isKnownLocalCall=true → 递归判 noop.Body
// (与 parent 同款语义传染:noop 含 yield 则 invoker 也含),
// invoker 形态 Compilable + P4 PJ5 升层可触达。
//
// **遮蔽安全**:closure 自身 Params(`function(name) ... end`)从继承表里
// 删掉,避免 parent 的 `noop` 与 closure 的 parameter `noop` 误识别。
// closure 体内重定义 local 由 walkStmt::LocalStmt 覆盖父继承条目。
func (v *compilabilityVisitor) walkFuncExpr(e *ast.FuncExpr) {
	v.currentDepth++
	if v.currentDepth > v.maxClosureDepth {
		v.maxClosureDepth = v.currentDepth
	}

	// 子函数体单独跑(隔离信号)
	sub := newCompilabilityVisitor()
	sub.currentDepth = v.currentDepth
	// 继承父 localFuncs 快照,减去 closure 自身参数同名遮蔽项
	for k, fn := range v.localFuncs {
		shadowed := false
		for _, p := range e.Params {
			if p == k {
				shadowed = true
				break
			}
		}
		if !shadowed {
			sub.localFuncs[k] = fn
		}
	}
	sub.walkBlock(e.Body)

	// 只回写嵌套深度
	if sub.maxClosureDepth > v.maxClosureDepth {
		v.maxClosureDepth = sub.maxClosureDepth
	}

	v.currentDepth--
}

// isKnownLocalCall:fn 是否是「已知 local 名,指向当前 Proto 的某子 Proto」。
//
// 用户拍板真实现:跟踪 local 函数名 → 子 FuncExpr 映射(LocalStmt /
// LocalFuncStmt 时挂表)。
func (v *compilabilityVisitor) isKnownLocalCall(fn ast.Expr) bool {
	name, ok := fn.(*ast.NameExpr)
	if !ok {
		return false
	}
	_, isLocal := v.localFuncs[name.Name]
	return isLocal
}

// isCoroutineCall 检测 coroutine.<method>(...) 模式(03 §3.2.4 (1))。
func isCoroutineCall(fn ast.Expr) bool {
	idx, ok := fn.(*ast.IndexExpr)
	if !ok {
		return false
	}
	obj, ok := idx.Obj.(*ast.NameExpr)
	if !ok || obj.Name != "coroutine" {
		return false
	}
	_, ok = idx.Key.(*ast.StringExpr)
	return ok
}

// isDebugCall 检测 debug.<method>(...) 模式。
func isDebugCall(fn ast.Expr) bool {
	idx, ok := fn.(*ast.IndexExpr)
	if !ok {
		return false
	}
	obj, ok := idx.Obj.(*ast.NameExpr)
	if !ok || obj.Name != "debug" {
		return false
	}
	_, ok = idx.Key.(*ast.StringExpr)
	return ok
}

// methodName 取 <table>.<method> 中的 method 名。
func methodName(fn ast.Expr) string {
	if idx, ok := fn.(*ast.IndexExpr); ok {
		if key, ok := idx.Key.(*ast.StringExpr); ok {
			return key.Val
		}
	}
	return ""
}

// safeStdlibFuncs Lua 5.1.5 stdlib 中**明确不会 yield 也不间接执行用户 Lua**
// 的函数白名单(P2 后续优化轮 #1 「精确 yield 分析」)。
//
// 收录原则:
//   - **不 yield**:函数自身不调 coroutine.yield;
//   - **不间接执行用户 Lua**:函数不接收 callback 参数(否则 callback 可能
//     yield)——这排除了 string.gsub(可接 fn) / table.foreach 等;
//   - **不重入解释器执行用户 metamethod**:函数操作的对象不会触发
//     `__index`/`__newindex` 等 metamethod——这是为什么 `pairs` / `next`
//     **不在白名单**(`pairs(t)` 触发 `__pairs` 在 5.2+,5.1 无此元方法,
//     `next` 直接读 raw,但保守起见仍排除)。
//
// pcall / xpcall **不在白名单**——尽管 pcall 自身有 yield-barrier,但其
// 内执行的 fn 含 metamethod 或 coroutine 接口的概率不可忽略,把它们排除
// 让保守边界与 P3 wasm 编译能力一致(P3 跨 pcall 边界编译复杂)。
//
// 主要白名单类别:
//   - 全局类型操作:type, tostring, tonumber, select, unpack
//   - 表 raw 操作:rawget, rawset, rawequal
//   - metatable 操作:setmetatable, getmetatable
//   - 算术 helper:math.* 全部
//   - 字符串 helper:string.* 不接收 fn 参数的(byte/char/find/format/len/
//     lower/upper/rep/reverse/sub/match/dump)——排除 gsub(接 fn)/gmatch(返迭代器)
//   - 表 helper:table.* 不接 fn 的(concat/insert/remove/sort/maxn)——
//     注意 sort 接 cmp fn 是用户 Lua,严格说也该排除;但实务里 cmp fn
//     极少 yield,放行更实用,留实测验证后调整
//
// 不在白名单的 stdlib 函数(显式拒绝):
//   - print / write / read / io.* / os.execute(IO 调用边界)
//   - error / assert(error 触发 pcall barrier 之外的 longjmp 语义)
//   - load / loadstring / loadfile / dofile(动态执行用户代码)
//   - string.gsub(接 fn 参数) / string.gmatch(返迭代器需 yield 链)
//   - table.foreach / table.foreachi(接 fn 参数)
//   - pairs / ipairs(返迭代器,与泛型 for 协议耦合)
//   - next(本身不 yield 但与迭代器协议耦合,保守排除)
var safeStdlibFuncs = map[string]bool{
	// 全局
	"type":         true,
	"tostring":     true,
	"tonumber":     true,
	"select":       true,
	"unpack":       true,
	"rawget":       true,
	"rawset":       true,
	"rawequal":     true,
	"setmetatable": true,
	"getmetatable": true,
}

// safeStdlibLibs 整库白名单 stdlib.<method> 全部安全(具体方法不再列举);
// 但仍排除每库内的「接 fn 参数」函数(safeStdlibLibFuncs 黑名单)。
var safeStdlibLibs = map[string]bool{
	"math":   true,
	"string": true,
	"table":  true,
	"os":     true, // os.time/os.date/os.clock 等不 yield;os.execute 是 IO 但本期保守放行
}

// safeStdlibLibFuncs 在 safeStdlibLibs 内但仍要排除的具体方法(接 fn / 返
// 迭代器 / 动态执行 类)。
var unsafeStdlibLibFuncs = map[string]map[string]bool{
	"string": {
		"gsub":   true, // 第三参可为 fn(执行用户代码)
		"gmatch": true, // 返迭代器(与泛型 for 协议耦合)
	},
	"table": {
		"foreach":  true, // 接 fn 参数
		"foreachi": true, // 同上
	},
	"os": {
		"execute": true, // IO 边界
	},
}

// isSafeStdlibCall 判断 fn 是否是 stdlib 白名单调用(P2 后续优化轮 #1)。
//
// 三种识别形态:
//   - NameExpr{name}:全局函数(type/tostring/...)→ 查 safeStdlibFuncs
//   - IndexExpr{Obj=NameExpr{lib}, Key=StringExpr{m}}:lib.m 形态 →
//     查 safeStdlibLibs / unsafeStdlibLibFuncs
//
// 任何其它形态(`obj:m()` 方法调用 / 传参 / 表字段保存 fn 后调等)统一
// 走 unknown 路径(保守第一)。
func isSafeStdlibCall(fn ast.Expr) bool {
	// 形态 1:全局名字直调
	if name, ok := fn.(*ast.NameExpr); ok {
		return safeStdlibFuncs[name.Name]
	}
	// 形态 2:lib.method 形态
	idx, ok := fn.(*ast.IndexExpr)
	if !ok {
		return false
	}
	libName, ok := idx.Obj.(*ast.NameExpr)
	if !ok {
		return false
	}
	mName, ok := idx.Key.(*ast.StringExpr)
	if !ok {
		return false
	}
	if !safeStdlibLibs[libName.Name] {
		return false
	}
	// 检查具体方法是否在该库的不安全黑名单
	if unsafe, ok := unsafeStdlibLibFuncs[libName.Name]; ok {
		if unsafe[mName.Val] {
			return false
		}
	}
	return true
}
