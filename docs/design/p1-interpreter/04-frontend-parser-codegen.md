# P1:语法分析 + 代码生成(寄存器分配)

> 状态:**设计阶段,可实现深度**。本文定义前端的后两段——**parser**(token 流 → AST)
> 与 **codegen**(AST → `bytecode.Proto`,含寄存器分配与常量折叠)。下游契约是
> [02-bytecode-isa](./02-bytecode-isa.md)(本文产出的 `Proto.Code` 必须严格服从其
> opcode 表、RK 编码、寄存器布局、比较指令成对、CLOSURE 伪指令、SETLIST/FPF=50);
> 产物对象布局见 [01-value-object-model](./01-value-object-model.md) §5.7。上游 token
> 流见 [03-frontend-lexer](./03-frontend-lexer.md)(尚未定稿,本文对其依赖在文末缺口标注)。
> 语言面限定 Lua 5.1(`roadmap.md` (§6),不做 5.2+ 的 goto/_ENV/整数/位运算)。

对应 Go 包:`internal/frontend/ast`、`internal/frontend/parse`、`internal/frontend/compile`。

---

## 1. 顶层决策:AST 双遍,而非官方单遍

Lua 官方实现(`lparser.c`/`lcode.c`)是 **single-pass**:parser 一边消费 token,一边
直接驱动 `luaK_*` 发射字节码,无独立 AST。本项目**选择 AST 中间层**(parser 产 AST,
codegen 独立消费),理由按本项目约束排序:

1. **差分测试要稳定可预测的寄存器分配**(`architecture.md` §4 不变式 2、`roadmap.md`
   (§5) 原则 2)。层间逐字节差分是 CI 必过门禁。AST 路线把"解析"与"分配寄存器"解耦,
   codegen 成为一个**纯函数 `Proto = Gen(ast)`**,无 I/O、无 lexer 前瞻耦合,寄存器分配
   结果只取决于 AST 形状——更易写**黄金测试**(固定 AST → 固定字节码)与**可复现性断言**。
2. **AST 利于 P2 可编译性分析**(`roadmap.md` (§4) P2「静态可编译性分析器」、(§5)
   原则 4)。P2 要静态识别 varargs/coroutine/debug 等"不升层"形状。在 AST 上做控制流/
   数据流分析远比在已铺平的字节码上做容易;AST 是 P2 分析器的天然输入,**P1 顺手产出、
   P2 直接复用**(P1 编译期可丢弃 AST,P2 需要时重新 parse 即可,二者用同一 parser)。
3. **可独立测试**(`architecture.md` §5 构建顺序第 7、8 步明确分列「parser + AST 单测,
   对拍官方 luac AST 形状」与「codegen + 寄存器分配」两个里程碑)。包布局 `architecture.md`
   §1 也已把 `ast/`、`parse/`、`compile/` 拆为三个独立包——本决策与既定包边界一致。
4. **错误定位更清晰**:语法错误在 parse 阶段集中报出(带 token 行号),codegen 阶段只剩
   编译期资源类错误(寄存器溢出、常量过多、控制结构过长),职责单一。

### 1.1 代价与缓解

| 代价 | 缓解 |
|---|---|
| 多一次 AST 物化(分配 + 遍历),编译比单遍慢、占内存多 | 编译是**一次性**的(`Compile→Program`,`roadmap.md` (§8));P1 不追编译速度,追**执行**速度与差分可控性。AST 节点可用 arena-free 的 Go 堆对象,编译后即可 GC |
| 无法直接套用官方 `lcode.c` 的单遍 `expdesc` 流 | **codegen 内部仍复用 Lua 5.1 的 `expdesc` + `freereg` 水位线机制**(§5),只是驱动者从 parser 换成 AST 遍历器。寄存器分配算法与官方同构,保证差分可比 |
| AST 与字节码两份真相,需保证一致 | AST 只承载**语法结构**,不承载寄存器决策;寄存器是 codegen 独占职责,无双真相 |

> **关键自洽承诺**:虽走 AST,但 codegen 的寄存器分配**算法**严格对齐 Lua 5.1
> `lcode.c`(freereg 水位线、局部变量绑定到连续低位寄存器、临时值落 freereg、`exp2RK`
> 折叠常量)。目标是产出与官方 luac **寄存器分配同构**的 `Proto`,使
> [12-testing-difftest](./12-testing-difftest.md) 的逐字节差分可行。本文 §10 端到端示例
> 与 [02-bytecode-isa](./02-bytecode-isa.md) §8 的 `f(n)` 寄存器分配**完全一致**即为自洽证据。

### 1.2 数据流总览

```
                  parse 包                         compile 包
 token 流 ──► 递归下降 parser ──► ast.Block ──► codegen(AST 遍历 + expdesc + freereg)
 (lex 包)        §4                  §3              §5..§9
                                                      │
                                                      ▼
                                          bytecode.Proto(§01 5.7)
                                          Code/Consts/Protos/UpvalDescs/MaxStack/...
```

---

## 2. 包职责边界

| 包 | 输入 | 输出 | 不做 |
|---|---|---|---|
| `internal/frontend/ast` | — | AST 节点类型定义(纯数据 struct) | 不含解析/生成逻辑 |
| `internal/frontend/parse` | `[]token.Token`(或 lexer 拉流) | `*ast.Block`(chunk 顶层) + 语法错误 | 不分配寄存器、不产字节码 |
| `internal/frontend/compile` | `*ast.Block` + 源名 | `*bytecode.Proto`(主 chunk,含嵌套 Protos) + 编译期错误 | 不解析 token |

`compile` 依赖 `ast`、`bytecode`、`value`(构造常量 Value)、`object`/`arena`(intern
字符串常量到 arena,产 GCRef)。`parse` 只依赖 `ast`、`token`、`lex`。**无环**,符合
`architecture.md` §3。

---

## 3. AST 节点定义(`internal/frontend/ast`)

AST 节点是**纯 Go 堆数据**(不入 arena,编译后可回收;对齐 [01](./01-value-object-model.md)
§1「不可变代码住 Go 堆」的精神,但 AST 比 Proto 更短命)。所有节点带 `Line int32`
(取自首 token,供错误与未来 `LineInfo`)。

### 3.1 统一接口与定位

```go
type Node interface{ Pos() int32 } // 返回起始源行

// 表达式与语句各自再分一层(便于 codegen 类型 switch)
type Expr interface{ Node; exprNode() }
type Stmt interface{ Node; stmtNode() }
```

> 用 sealed-interface(空方法 `exprNode()/stmtNode()`)而非裸 `interface{}`,让 codegen
> 的 `switch e := e.(type)` 穷尽性更易审查,也防止误把语句塞进表达式位置。

### 3.2 表达式节点(覆盖任务清单全部 12 类)

```go
// —— 字面量 ——
type NilExpr    struct{ Line int32 }                 // nil
type TrueExpr   struct{ Line int32 }                 // true
type FalseExpr  struct{ Line int32 }                 // false
type NumberExpr struct{ Line int32; Val float64 }    // 数字(Lua 5.1 全是 double)
type StringExpr struct{ Line int32; Val string }     // 字符串字面量(Go string,codegen 时 intern)
type VarargExpr struct{ Line int32 }                 // ...  (仅 vararg 函数内合法)

// —— 变量/前缀表达式 ——
type NameExpr struct {                                // 标识符:codegen 解析为 local / upval / global
    Line int32
    Name string
}
type IndexExpr struct {                               // prefix[key] 或 prefix.field
    Line   int32
    Obj    Expr                                       // 前缀表达式
    Key    Expr                                       // a.b 形如 Key=StringExpr{"b"}
}

// —— 调用 ——
type CallExpr struct {                                // f(args...)
    Line int32
    Fn   Expr
    Args []Expr                                       // 末位可为多值表达式(Call/Vararg)
}
type MethodCallExpr struct {                          // obj:m(args...) → SELF
    Line   int32
    Recv   Expr
    Method string                                     // 方法名(进常量池)
    Args   []Expr
}

// —— 运算 ——
type BinExpr struct {                                 // a op b
    Line int32
    Op   BinOp                                        // 见 §3.4
    L, R Expr
}
type UnExpr struct {                                  // op a  (not / # / -)
    Line int32
    Op   UnOp
    E    Expr
}

// —— 函数字面量 ——
type FuncExpr struct {                                // function(params) body end
    Line     int32
    Params   []string
    IsVararg bool
    Body     *Block
    EndLine  int32                                    // 供 LineInfo / 调试
}

// —— table 构造 ——
type TableExpr struct {                               // { ... }
    Line   int32
    AKeys  []Expr                                     // 数组部分:无键项,按出现序(末位可多值)
    HKeys  []Expr                                     // 哈希部分键(与 HVals 等长)
    HVals  []Expr                                     // 哈希部分值;[k]=v 与 name=v 统一规约到此
}
```

设计注记:
- `IndexExpr` 同时覆盖 `a.b`(语法糖:`Key` 是 `StringExpr`)与 `a[expr]`,codegen 不必区分。
- `CallExpr` / `MethodCallExpr` **既是 Expr 又能作语句**:作语句时丢弃返回值(`C=1`),
  作表达式末位时按上下文要 1 值或多值(§7)。
- `TableExpr` 把三种字段(`[k]=v`、`name=v`、纯值)在 parser 阶段就**规约成两类**:进
  数组的 `AKeys`(纯值,按序)与进哈希的 `HKeys/HVals`(`name=v` 规约为 `HKeys=StringExpr`)。
  这样 codegen 直接对应 ISA 的 `SETLIST`(数组批量)+ `SETTABLE`(哈希逐个)。

### 3.3 语句节点(覆盖任务清单全部 13 类)

```go
type Block struct {                                   // 语句序列 + 作用域单元
    Stmts []Stmt
}

type LocalStmt struct {                               // local a, b = e1, e2
    Line  int32
    Names []string
    Exprs []Expr                                       // 可空(local a → 初始化为 nil)
}
type LocalFuncStmt struct {                           // local function f() ... end
    Line int32
    Name string
    Fn   *FuncExpr
}
type AssignStmt struct {                               // lhs1, lhs2 = rhs1, rhs2
    Line    int32
    Targets []Expr                                     // 每项必是 NameExpr 或 IndexExpr(parser 校验)
    Exprs   []Expr
}
type CallStmt struct {                                 // f()  /  obj:m()  作为独立语句
    Line int32
    Call Expr                                          // CallExpr 或 MethodCallExpr
}
type DoStmt    struct{ Line int32; Body *Block }       // do ... end
type WhileStmt struct{ Line int32; Cond Expr; Body *Block }
type RepeatStmt struct {                               // repeat ... until cond
    Line int32
    Body *Block                                        // 注意:until 的 cond 在 Body 作用域内可见局部
    Cond Expr
}
type IfStmt struct {                                   // if / elseif* / else?
    Line    int32
    Clauses []IfClause                                 // 每个 elseif 一个 clause;最后 Else 可空
    Else    *Block
}
type IfClause struct{ Cond Expr; Body *Block }

type NumForStmt struct {                               // for v = init, limit [, step] do
    Line  int32
    Var   string
    Init  Expr
    Limit Expr
    Step  Expr                                         // 可空 → 默认 1
    Body  *Block
}
type GenForStmt struct {                               // for a,b,... in explist do
    Line  int32
    Names []string
    Exprs []Expr                                       // 迭代器三元组来源(explist,可少于3,补 nil)
    Body  *Block
}
type FuncStmt struct {                                 // function a.b.c:m() ... end
    Line     int32
    Target   Expr                                      // 解析成 NameExpr / IndexExpr 链
    IsMethod bool                                      // a.b:m → 给 Fn 注入隐式 self 形参
    Fn       *FuncExpr
}
type ReturnStmt struct {                               // return [explist]
    Line  int32
    Exprs []Expr                                       // 空 = return 无值;末位可多值;尾调用见 §9
}
type BreakStmt struct{ Line int32 }                    // break
```

设计注记:
- `function a.b:m()` 是语法糖:parser 把它降解为 `Target = IndexExpr(a.b, "m")` 且
  `IsMethod=true`,codegen 给 `Fn.Params` 前置一个隐式 `"self"`(等价
  `a.b.m = function(self) ... end`)。`local function f` 单列为 `LocalFuncStmt`,因为它
  **先声明局部再赋值**(允许函数体内递归引用自身),语义不同于 `local f = function`。
- `RepeatStmt` 特殊:`until` 的条件表达式处于循环体作用域**内**(Lua 5.1 语义),局部变量
  在 cond 求值时仍活跃——codegen 的作用域退出必须延后到 cond 之后(§6.4)。

### 3.4 运算符枚举

```go
type BinOp uint8
const (
    OpAdd BinOp = iota; OpSub; OpMul; OpDiv; OpMod; OpPow   // 算术
    OpConcat                                                // ..
    OpEq; OpNe; OpLt; OpLe; OpGt; OpGe                       // 关系
    OpAnd; OpOr                                              // 逻辑(短路)
)
type UnOp uint8
const ( OpUnm UnOp = iota; OpNot; OpLen )                    // -  not  #
```

注意 `OpNe/OpGt/OpGe` 在 ISA 中没有独立 opcode:codegen 通过**交换操作数 + 取反期望布尔**
映射到 `EQ/LT/LE`(§5.5)。`OpAnd/OpOr` 不产算术指令,走短路跳转(§5.6)。

---

## 4. 递归下降 parser(`internal/frontend/parse`)

### 4.1 主结构

```go
type Parser struct {
    lx        *lex.Lexer    // token 源(见 03 文档)
    tok       token.Token   // 当前 token(lookahead 1)
    ahead     token.Token   // 预读缓存(部分产生式需 LL(2),如区分 'a.b=' vs 'a.b()')
    hasAhead  bool
    source    string        // 源名(chunk name),用于错误
}

func Parse(lx *lex.Lexer, source string) (*ast.Block, error)  // 顶层入口:解析整个 chunk
```

Lua 5.1 文法基本是 **LL(1)**;唯一需要小前瞻的是「赋值语句 vs 函数调用语句」(都以
`prefixexp` 开头,看其后是 `=`/`,` 还是结束)——用 `ahead` 一个 token 解决,或解析完
`prefixexp` 后按下一个 token 决策(§4.4)。

### 4.2 语句/表达式解析函数划分

每个非终结符一个方法,镜像 Lua 5.1 `lparser.c` 的函数族:

```
// —— 语句层 ——
parseBlock()        → *ast.Block         // statlist:循环 parseStatement 直到 block 终结符
parseStatement()    → ast.Stmt           // 按首 token 分派(见下表)
parseLocal()        → LocalStmt/LocalFuncStmt
parseIf()           → IfStmt             // if cond then block {elseif} [else] end
parseWhile()        → WhileStmt
parseRepeat()       → RepeatStmt
parseNumOrGenFor()  → NumForStmt/GenForStmt  // for Name '=' → 数值;for Namelist 'in' → 泛型
parseFunctionStmt() → FuncStmt           // function funcname funcbody
parseReturn()       → ReturnStmt
parseDo()           → DoStmt
parseExprStmt()     → AssignStmt/CallStmt // 以 prefixexp 起头,§4.4 决策

// —— 表达式层(优先级爬升)——
parseExpr(limit)    → ast.Expr           // 优先级驱动的二元/一元(§4.3)
parseSimpleExpr()   → ast.Expr           // 字面量 / table / function / prefixexp
parsePrefixExpr()   → ast.Expr           // Name 或 '(' expr ')' 起头,后接 .field/[k]/args/:m
parsePrimaryExpr()  → ast.Expr           // Name | '(' expr ')'
parseTableExpr()    → TableExpr
parseFuncBody(isMethod) → *FuncExpr      // '(' parlist ')' block 'end'
parseArgs()         → []ast.Expr         // '(' explist ')' | tablector | string
parseExprList()     → []ast.Expr         // expr {',' expr}
```

`parseStatement()` 首 token 分派表(对齐 Lua 5.1 `statement()`):

| 首 token | 产生式 |
|---|---|
| `local` | `parseLocal`(再看是否 `function`) |
| `if` | `parseIf` |
| `while` | `parseWhile` |
| `do` | `parseDo` |
| `for` | `parseNumOrGenFor` |
| `repeat` | `parseRepeat` |
| `function` | `parseFunctionStmt` |
| `return` | `parseReturn`(必为 block 末句) |
| `break` | `BreakStmt`(必为 block 末句,Lua 5.1 限制) |
| `;` | 空语句,跳过 |
| 其它(`Name`/`(`) | `parseExprStmt` |

block 终结符(停止 `parseBlock` 循环):`end` / `else` / `elseif` / `until` / `<EOF>`。

### 4.3 表达式解析:优先级爬升(precedence climbing)

不用为每个优先级层写一个函数(`orexpr→andexpr→...`),而用单个 `parseExpr(limit)` 带
**绑定优先级表**(Lua 5.1 `subexpr` 同款),更短且与官方完全对齐:

```go
// 二元运算符的 (左结合优先级, 右结合优先级)。右<左 ⇒ 左结合;右>左 ⇒ 右结合。
var binPrio = map[BinOp]struct{ left, right uint8 }{
    OpOr:  {1, 1},  OpAnd: {2, 2},
    OpLt:  {3, 3},  OpGt: {3, 3}, OpLe: {3, 3}, OpGe: {3, 3}, OpNe: {3, 3}, OpEq: {3, 3},
    OpConcat: {5, 4},                  // 右结合:right(4) < left(5)
    OpAdd: {6, 6}, OpSub: {6, 6},
    OpMul: {7, 7}, OpDiv: {7, 7}, OpMod: {7, 7},
    // 一元运算符优先级 = 8(UNARY_PRIORITY),见下
    OpPow: {10, 9},                    // 右结合:right(9) < left(10),且高于一元
}
const unaryPriority = 8

func (p *Parser) parseExpr(limit uint8) ast.Expr {
    var e ast.Expr
    if op, ok := p.unaryOp(); ok {              // 前缀一元:not / # / -
        line := p.tok.Line; p.next()
        sub := p.parseExpr(unaryPriority)        // 一元绑 8
        e = &ast.UnExpr{Line: line, Op: op, E: sub}
    } else {
        e = p.parseSimpleExpr()
    }
    for {                                        // 左结合循环
        op, ok := p.binOp()
        if !ok || binPrio[op].left <= limit { break }
        p.next()
        rhs := p.parseExpr(binPrio[op].right)     // 用右优先级递归 ⇒ 控制结合性
        e = &ast.BinExpr{Op: op, L: e, R: rhs}
    }
    return e
}
```

### 4.4 赋值 vs 调用语句的消歧

`parseExprStmt` 先调 `parsePrefixExpr` 得到一个表达式 `e`,再看当前 token:

- 若是 `=` 或 `,` → 这是赋值语句。`e` 必须是 `NameExpr` 或 `IndexExpr`(否则报
  `cannot assign to ...` / `syntax error`);继续收集逗号分隔的其余 LHS 与 `=` 后的 RHS。
- 否则 `e` 必须是 `CallExpr` 或 `MethodCallExpr` → `CallStmt`(独立调用语句);其它形状报
  `syntax error`(如裸 `a + b` 作语句)。

这与 Lua 5.1 `exprstat()` 逻辑一致:`prefixexp` 解析后由后继符号定夺语句种类。

---

## 5. 代码生成与寄存器分配(`internal/frontend/compile`)—— 本文重点

codegen 是**栈式寄存器分配的水位线模型**,直接移植 Lua 5.1 `lcode.c`/`lparser.c` 的核心
不变式,只把"驱动者"从单遍 parser 换成 AST 后序遍历。

### 5.1 FuncState:每函数一个编译状态

每个被编译的函数(主 chunk 与每个 `FuncExpr`)有独立 `FuncState`,嵌套函数通过 `prev`
链接(供 upvalue 解析,§8):

```go
type funcState struct {
    proto    *bytecode.Proto    // 正在产出的 Proto(§01 5.7)
    prev     *funcState         // 外层函数(闭包嵌套链)
    cg       *codegen           // 全局编译上下文(常量 intern、错误收集、ProtoID 分配)

    freereg  int                // ★ 寄存器水位线:下一个空闲寄存器号
    nactvar  int                // 当前活跃局部变量数(= 它们占据 R(0..nactvar-1))
    actvar   []int              // 活跃局部 → locvars 索引(作用域出栈时回缩)
    locvars  []localVar         // 所有局部变量描述(名字 + 活跃区间,供调试/upvalue)
    upvals   []upvalDesc        // 本函数的 upvalue 来源(产 Proto.UpvalDescs)

    bl       *blockCnt          // 当前块链(break 列表、作用域 nactvar 快照、upval 标志)
    jpc      int                // "jump-to-pc" 待回填链:目标是下一条将发射指令(§5.4)
    lastTarget int              // 最近一个被标记为跳转目标的 pc(防止误删指令的优化)
    nk       int                // 常量计数(= len(proto.Consts))
}

type localVar struct{ name string; startpc, endpc int32 } // 活跃区间 [startpc, endpc)
type blockCnt struct {
    prev        *blockCnt
    breakList   int             // 本可 break 块的 break 跳转链(NoJump=空)
    nactvarSnap int             // 进块时 nactvar(出块时恢复 ⇒ 释放块内局部)
    isLoop      bool            // 是否为可 break 的循环块(while/repeat/for)
    hasUpval    bool            // 块内是否有局部被内层闭包捕获 ⇒ 出块发 CLOSE
}
```

`freereg` 与 `nactvar` 的**核心不变式**(出块时断言,见 §6.1):
> 进入/退出任何块时 `freereg == nactvar`——即"块边界上没有悬空临时寄存器"。

### 5.2 表达式描述符 expdesc:延迟物化的关键

codegen **不立即**把每个子表达式塞进寄存器,而是先求出一个 `expdesc`(描述"这个值现在在
哪、怎么取"),到需要时(要进寄存器 / 要作 RK / 要参与跳转)才**discharge**(物化)。这是
Lua 5.1 用最少指令、最优常量折叠的核心机制,本项目原样复用:

```go
type expKind uint8
const (
    EVoid     expKind = iota // 无值(空表达式列表占位)
    ENil                     // 字面 nil
    ETrue; EFalse            // 字面布尔
    EKNum                    // 数字字面量,值在 e.nval(尚未入常量池)
    EK                       // 已在常量池,e.info = K 索引
    ELocal                   // 局部变量,e.info = 寄存器号
    EUpval                   // upvalue,e.info = upvalue 索引
    EGlobal                  // 全局,e.info = 名字的 K 索引
    EIndexed                 // t[k]:e.info = 表寄存器, e.aux = 键 RK
    EJmp                     // 比较指令已发射(EQ/LT/LE),e.info = 该指令 pc(其后续 JMP 待定)
    ERelocable               // 结果寄存器未定的指令(如 GETGLOBAL/GETTABLE/NEWTABLE/CALL单值),e.info = 指令 pc,可改写其 A
    ENonReloc                // 结果已固定在某寄存器,e.info = 寄存器号
    ECall                    // 函数调用,e.info = CALL 指令 pc(可调 C 取多值)
    EVararg                  // ... ,e.info = VARARG 指令 pc
)

type expDesc struct {
    k        expKind
    info     int       // 含义随 k(见上)
    aux      int       // EIndexed 的键 RK
    nval     float64   // EKNum 的数字
    tJmp     int       // "为真则跳"的回填链(逻辑表达式;NoJump=空)
    fJmp     int       // "为假则跳"的回填链
}
```

`expdesc` 的 `t`/`f`(true/false patch list)只在逻辑表达式(`and`/`or`/比较)里非空,承载
短路跳转的回填链(§5.6)。

### 5.3 寄存器分配原语

直接对应 `lcode.c` 的 `luaK_reserveregs`/`luaK_checkstack`/`exp2nextreg` 等:

```go
func (fs *funcState) reserveRegs(n int)        // freereg += n,并更新 MaxStack 水位
func (fs *funcState) checkStack(n int)          // 确保 freereg+n ≤ 250,否则报错(§9 寄存器溢出)
func (fs *funcState) freeReg(r int)             // 若 r 是临时(≥nactvar 且为栈顶),freereg--
func (fs *funcState) freeExp(e *expDesc)        // 若 e 是 ENonReloc 的临时寄存器,归还

func (fs *funcState) exp2NextReg(e *expDesc)    // 把 e 物化到 freereg,并 reserveRegs(1) ⇒ e 变 ENonReloc
func (fs *funcState) exp2AnyReg(e *expDesc) int // 把 e 物化到某寄存器(若已在寄存器则原地),返回寄存器号
func (fs *funcState) exp2Val(e *expDesc)        // 若 e 带未决跳转链,先合流物化为具体值
func (fs *funcState) exp2RK(e *expDesc) int     // 尽量折叠为 K(返回 256+idx);否则 exp2AnyReg(返回寄存器号)
func (fs *funcState) dischargeVars(e *expDesc)  // 把 EGlobal/EUpval/ELocal/EIndexed 变成可取值形式
```

**`MaxStack` 计算**:每次 `reserveRegs` 后 `proto.MaxStack = max(proto.MaxStack, freereg)`。
编译结束时 `MaxStack` 即该函数所需寄存器数(写入 `Proto.MaxStack`,供解释器进帧时备栈,
[01](./01-value-object-model.md) §5.7 / [05](./05-interpreter-loop.md))。下界是
`NumParams`(+vararg 不占正寄存器),并保证 `≥2`(Lua 5.1 留 `LUA_MINSTACK` 余量;调用至少
需放函数+1参)。

**`exp2RK` 的常量折叠**(直接兑现 [02](./02-bytecode-isa.md) §2 的 RK 编码):
- `ENil/ETrue/EFalse/EKNum/EK` → 调 `addConst` 取得 K 索引 `kidx`;若 `kidx < 256` 则返回
  `256+kidx`(RK 常量形式),算术/比较/SETTABLE 等可直接吃常量,省一条 LOADK。
- 若 `kidx ≥ 256`(常量太多)→ 退化为 `exp2AnyReg` 先 `LOADK` 进寄存器再用(见
  [02](./02-bytecode-isa.md) §5「超出部分改用 LOADK」)。
- 非常量(变量/调用结果)→ `exp2AnyReg`。

### 5.4 跳转回填机制(jump list / patch list)

完全采用 Lua 5.1 的**链表嵌指令流**法(无辅助内存):未决前向跳转的 `JMP` 指令的 `sBx`
字段**临时存"链中下一个待回填 JMP 的相对偏移"**,`NoJump = -1` 作链尾哨兵。

```go
const NoJump = -1

func (fs *funcState) jump() int                 // 发 JMP(sBx=链入 jpc 后清空 jpc),返回该 JMP 的 pc
func (fs *funcState) getJump(pc int) int        // 读 pc 处 JMP 的链接,返回链中下一 pc 或 NoJump
func (fs *funcState) fixJump(pc, dest int)       // 把 pc 处 JMP 的 sBx 改写为指向 dest 的相对偏移
func (fs *funcState) concat(l1 *int, l2 int)     // 把跳转链 l2 接到 *l1 尾部
func (fs *funcState) patchList(list, target int) // 把整条链 list 全部回填到 target
func (fs *funcState) patchToHere(list int)       // 把 list 合并进 jpc(目标=下一条将发射指令)
func (fs *funcState) getLabel() int              // 返回当前 pc 并标记为跳转目标(lastTarget=pc)
```

**`jpc`(jump-to-pc)**:目标恰好是"下一条将要发射的指令"的待决跳转链。每次**真正发射**一条
新指令前,`dischargeJpc` 把 `jpc` 链全部 `fixJump` 到当前 `pc` 再清空。这让"跳到这里"
(`patchToHere`)无需知道未来 pc——延迟到那条指令真出现时自动落定。

**`fixJump` 越界检查**:`offset = dest-(pc+1)`,若 `|offset| > MaxArgSBx(131071)` 报
`control structure too long`(对齐 Lua 5.1 措辞,§9)。

**带值跳转链的回填(`patchListAux` 的双目标)**:逻辑表达式的跳转链里混有两种 JMP:
- 来自 `TESTSET` 的——命中时要**把值存进结果寄存器**再跳到 `vtarget`;
- 普通控制跳转——跳到 `dtarget`。

`patchListAux(list, vtarget, reg, dtarget)` 遍历链:对每个节点用 `patchTestReg(node, reg)`
判定——若该 JMP 前驱是 `TESTSET` 且需要落值,改其 A=reg 并回填到 `vtarget`;否则(前驱非
`TESTSET`,或寄存器已是该值)把 `TESTSET` 退化为 `TEST` 并回填到 `dtarget`。这正是
[02](./02-bytecode-isa.md) §4 中 `TEST`/`TESTSET` 成对设计的消费方。

### 5.5 算术 / 比较表达式

**算术**(`BinExpr{OpAdd..OpPow}` / `UnExpr{OpUnm,OpLen}`):后序遍历——先 codegen 左、右子
表达式得 `el`/`er`,各调 `exp2RK` 得 RK 操作数,发对应指令,结果设为 `ERelocable`(指令 A
待定,留给上层 `exp2NextReg` 决定落哪),并归还被吃掉的临时寄存器:

```
// e = a + b
codegen(a) → el ;  rb := exp2RK(el)
codegen(b) → er ;  rc := exp2RK(er)
freeExp(er); freeExp(el)                 // 先归还高位再低位(维持栈式)
emit ADD A=0(待定) B=rb C=rc → pc
e.k = ERelocable ; e.info = pc           // 上层 exp2NextReg 时回填 A
```

**常量折叠(编译期)**:若 `a`、`b` 都是数字字面量(`EKNum`),codegen 直接算出结果(`+ - *
/ % ^` 按 [02](./02-bytecode-isa.md) §4 的 Lua 语义:MOD=`a-floor(a/b)*b`,POW=math.pow,
DIV 浮点),令 `e` 为 `EKNum`,**不发指令**。对齐 Lua 5.1 `constfolding`,且需保证折叠结果与
运行期 NaN 规范化一致([01](./01-value-object-model.md) §3.4:折叠产 NaN 也归一为 canonNaN)。

**比较**(`OpEq/OpNe/OpLt/OpLe/OpGt/OpGe`):映射到 ISA 仅有的 `EQ/LT/LE`(成对 JMP),用
**操作数交换 + 期望布尔取反**覆盖 6 个关系:

| AST 运算 | 发射 | 说明 |
|---|---|---|
| `a == b` | `EQ A=1 B=rk(a) C=rk(b)` | 期望"相等为真" |
| `a ~= b` | `EQ A=0 B=rk(a) C=rk(b)` | 期望布尔取反 |
| `a < b`  | `LT A=1 B=rk(a) C=rk(b)` | |
| `a > b`  | `LT A=1 B=rk(b) C=rk(a)` | **交换操作数**(无 GT) |
| `a <= b` | `LE A=1 B=rk(a) C=rk(b)` | |
| `a >= b` | `LE A=1 B=rk(b) C=rk(a)` | 交换操作数 |

比较指令发射后**紧跟一条 JMP**(`e.k = EJmp`,`e.info` = 比较指令 pc;真假分支的回填链记在
`e.t`/`e.f`)。如何把"比较结果"物化成布尔值(`if/while` 不需要,但 `local x = a<b` 需要)见
§5.7 的 `exp2reg` + `LOADBOOL` 物化。这严格满足 [02](./02-bytecode-isa.md) §9 不变式 3
「比较指令后必跟 JMP」。

### 5.6 逻辑 and / or 短路

`and`/`or` 不产值指令,而是操纵 `expdesc` 的 `t`/`f` 跳转链 + `TEST`/`TESTSET`:

```
// a and b  : a 为假则整体为假(短路),跳过 b
codegen(a) → e
goIfTrue(e)                  // 发 TESTSET/TEST + JMP:a 为真则继续(落到 b),为假则跳转(链入 e.f)
                             //   并把 e.t patchToHere(a 为真的去向 = b 的起点)
codegen(b) → e2
// 合流:整体的 f 链 = e.f(a 假)∪ e2.f;整体的 t 链 = e2.t
concat(&e2.f, e.f)
e = e2

// a or b   : a 为真则整体为真(短路),跳过 b   —— 对称,用 goIfFalse
codegen(a) → e
goIfFalse(e)                 // a 为假则继续(落到 b),为真则跳转(链入 e.t);e.f patchToHere
codegen(b) → e2
concat(&e2.t, e.t)
e = e2
```

`goIfTrue(e)`/`goIfFalse(e)`(对齐 `luaK_goiftrue`/`goiffalse`):
- 若 `e` 已是比较(`EJmp`),直接用其 JMP;
- 否则发 `TESTSET R=结果reg, B=源, C=期望布尔` + `JMP`,把这条 JMP 链入 `e.t`(或 `e.f`)。
  `TESTSET` 在最终物化时若发现"无处存值/值已就位"会被 `patchTestReg` 退化为 `TEST`(§5.4)。

最终把整个逻辑表达式物化到寄存器时(§5.7),用 `patchListAux` 把 `t`/`f` 两条链分别落到
"真值位置"和"假值位置",必要时补 `LOADBOOL`(见下)。

### 5.7 把带跳转的表达式物化为寄存器值(`exp2reg`)

当逻辑/比较表达式必须落成一个具体寄存器值(赋值 RHS、函数实参、return 值)时:

```go
func (fs *funcState) exp2reg(e *expDesc, reg int) {
    fs.dischargeToReg(e, reg)               // 把非跳转部分先落到 reg(ENonReloc)
    if e.k == EJmp { fs.concat(&e.t, e.info) } // 比较自身的 JMP 计入 t 链
    if e.hasJumps() {                        // t/f 链非空 ⇒ 需要物化布尔
        // 若链里有"非 TESTSET"的普通跳转,需要两条 LOADBOOL 落具体 true/false
        pf := fs.loadBoolFalse(reg)          // LOADBOOL reg,0,1  (load false 并跳过下一条)
        pt := fs.loadBoolTrue(reg)           // LOADBOOL reg,1,0  (load true)
        final := fs.getLabel()
        fs.patchListAux(e.f, final, reg, pf) // 假链 → load-false 处
        fs.patchListAux(e.t, final, reg, pt) // 真链 → load-true 处
    }
    e.f, e.t = NoJump, NoJump
    e.k, e.info = ENonReloc, reg
}
```

这对应 [02](./02-bytecode-isa.md) §4 `LOADBOOL` 的 "C≠0 则 pc++" 语义:`load false; (skip)
load true` 两条配合一个落点,把"比较/短路结果"变成寄存器里的 `true`/`false`。仅当链中含
非 `TESTSET` 跳转(`need_value`)才需要这对 `LOADBOOL`;纯 `TESTSET` 链可直接 patch 落值。

### 5.8 局部变量声明与作用域

```
local a, b = e1, e2
```
codegen 步骤(对齐 `localstat`):
1. 先把 RHS `e1,e2` 求值并**调整到 LHS 个数**(§6.2 多值规约):依次 `exp2NextReg`,使值
   落在 `R(freereg)` 起的连续寄存器;不足补 `LOADNIL`,多余丢弃。
2. **声明在求值之后**(关键!):`local a=a` 中右侧 `a` 是外层的;故先求 RHS 再
   `registerLocal("a")`、`registerLocal("b")`,使 `nactvar += 2`,把刚求值的两个寄存器
   "认领"为局部变量 `a`(R(freereg-2))、`b`(R(freereg-1))。
3. 设置 `locvars[i].startpc = pc`(活跃区间起点)。

`local function f`(`LocalFuncStmt`)反之:**先**声明局部 `f`(占一个寄存器),**再** codegen
其 `FuncExpr`(CLOSURE 落到该寄存器),使函数体内可引用 `f` 自身(递归)。

**作用域 = block**:`do/while/for/if-branch/repeat/函数体` 各开一个 `blockCnt`。

### 5.9 局部变量活跃区间与寄存器复用

`nactvar` 是"当前可见局部数",它们占 `R(0..nactvar-1)`,`freereg` 从 `nactvar` 起向上是临时
区。块退出时 `nactvar` 回缩、`freereg = nactvar`,**块内局部占用的寄存器被回收复用**:

```
do local x = 1 end   -- x 占 R0,块退出后 R0 释放
do local y = 2 end   -- y 复用 R0
```

`removeVars(level)`:把 `locvars[actvar[level..]]` 的 `endpc = pc`(闭合活跃区间,供
`LineInfo`/调试),`nactvar = level`。活跃区间 `[startpc,endpc)` 写入 Proto 调试信息
([01](./01-value-object-model.md) §5.7 的 `LineInfo` 旁,局部变量表实现时落同一调试段)。

---

## 6. 控制结构的 codegen 模式

下列模式产出的字节码与 [02](./02-bytecode-isa.md) §4/§6 的 opcode 语义、寄存器布局逐条对应。

### 6.1 块进出与 CLOSE

```go
func (fs *funcState) enterBlock(isLoop bool) {
    fs.bl = &blockCnt{prev: fs.bl, breakList: NoJump,
                      nactvarSnap: fs.nactvar, isLoop: isLoop}
    assert(fs.freereg == fs.nactvar)            // 进块不变式
}
func (fs *funcState) leaveBlock() {
    bl := fs.bl
    fs.removeVars(bl.nactvarSnap)               // 闭合块内局部活跃区间,nactvar 回缩
    if bl.hasUpval { fs.emitClose(bl.nactvarSnap) } // 有被捕获的局部 ⇒ CLOSE 关闭开放 upvalue
    fs.bl = bl.prev
    fs.freereg = fs.nactvar                     // 出块不变式:释放块内临时
    if bl.isLoop { fs.patchToHere(bl.breakList) } // break 全部落到块后
    assert(fs.freereg == fs.nactvar)
}
```

`CLOSE` 仅在"块内有局部被内层闭包按引用捕获"(`hasUpval`)时发射,对应
[02](./02-bytecode-isa.md) §4 `CLOSE` 语义(关闭 ≥R(A) 的开放 upvalue)。`hasUpval` 由 §8 的
upvalue 解析在发现"捕获外层栈局部"时回溯置位。

### 6.2 多值规约(adjust)

把一串表达式调整到目标个数 `nWant`(LHS 个数 / for 三槽 / 调用参数):
- 末位表达式是**多值源**(`ECall`/`EVararg`):设其取值数 = `nWant - (前面已落的固定值数)`,
  通过改写 CALL/VARARG 的 `C`(或 `B`)字段实现(`C=want+1`;`want` 可变时 `C=0` 到 top)。
- 其余表达式逐个 `exp2NextReg`(每个产 1 值)。
- 固定值不足 `nWant` → 补 `LOADNIL`;超出 → 自然丢弃(只 `exp2NextReg` 前 nWant 个,余者求值
  副作用仍执行但结果丢弃,对齐 Lua 语义)。

这是 [02](./02-bytecode-isa.md) §3「B/C=0 表示到 top」的发射侧来源。

### 6.3 if / elseif / else

```
if c1 then b1 elseif c2 then b2 else b3 end
```
```
codegen cond c1 (作条件) → 假则跳 (falseJmp1)
  block b1
  JMP → endList                  ; b1 末尾跳过其余分支
patch falseJmp1 to here          ; c1 假的落点 = elseif 测试
codegen cond c2 → 假则跳 (falseJmp2)
  block b2
  JMP → endList
patch falseJmp2 to here
  block b3                        ; else(无则空)
patch endList to here            ; 所有分支汇合
```
"作条件"= `codegenCondExpr(cond)`:对 cond 求 expdesc 后 `goIfFalse`(§5.6),取其 `f` 链作为
"假则跳"的待回填链;`t` 链 `patchToHere`(真则落入分支体)。`endList` 用 `concat` 累积各
分支末尾的无条件 JMP。最后一个分支(else 或末 elseif 体)后**不发** JMP。

### 6.4 while 与 repeat-until

**while**:
```
L_top := getLabel()
codegen cond → 假则跳 (exitList)
enterBlock(isLoop=true)
  block body
JMP → L_top                       ; 回边
leaveBlock()                      ; 释放 body 局部;patch break 到此
patch exitList to here
```
回边 `JMP → L_top` 是 [02](./02-bytecode-isa.md) §4「循环里向后 JMP」的 P2 热度采样点。

**repeat-until**(`until` 条件在 body 作用域内):
```
L_top := getLabel()
enterBlock(isLoop=true)           ; ★ 注意:body 与 cond 同块
  block body
  codegen cond (在 body 作用域内,局部仍可见) → 真则跳 ... 
  // until 语义:cond 为真则退出。即"假则回跳 L_top",真则落出
  patch condFalseList to L_top    ; cond 假 → 回到循环顶
leaveBlock()                      ; cond 求值后才退局部作用域;patch break 到此
```
关键差异:`leaveBlock` 的 `removeVars`/`freereg` 复位**必须在 cond 求值之后**,因为 Lua 5.1
允许 `repeat local x=f() until x>0`。若块内有 upvalue 捕获,`CLOSE` 也在 cond 之后。

### 6.5 数值 for(FORPREP / FORLOOP 四寄存器)

`for v = init, limit, step do body end` 占连续 4 槽 `R(A..A+3)`,布局严格按
[02](./02-bytecode-isa.md) §6:`R(A)`=内部索引(init)、`R(A+1)`=limit、`R(A+2)`=step、
`R(A+3)`=可见循环变量 `v`。

```
base := fs.freereg
exp2NextReg(init)                 ; R(base+0)  ("(for index)" 内部名)
exp2NextReg(limit)                ; R(base+1)  ("(for limit)")
if step==nil { LOADK R(base+2),K(1) } else { exp2NextReg(step) } ; R(base+2) ("(for step)")
reserveRegs(1)                    ; 预留 R(base+3) 给 v(尚未赋值)
prep := emit FORPREP base, sBx=待定
enterBlock(isLoop=true)
  registerLocal(v) → 绑定到 R(base+3) ; nactvar++,v 在 body 内可见
  block body
leaveBlock()
loop := emit FORLOOP base, sBx=(prep+1 - (loop+1))  ; 回跳到 FORPREP 之后第一条
fixJump(prep, loop)               ; FORPREP 跳到 FORLOOP
```
`init/limit/step` 这三个内部槽是**匿名局部**(名字 `(for index)` 等,不可见但占 nactvar,
防止 body 内临时复用它们)。`FORPREP` 预减一个 step 后跳到 `FORLOOP`,`FORLOOP` 加 step、
判界、回跳并刷新 `v`(语义见 [02](./02-bytecode-isa.md) §4 行 31/32)。三槽非 number 由
`FORPREP` 运行期报错 `'for' initial value must be a number`(codegen 不静态检查类型)。

### 6.6 泛型 for(TFORLOOP)

`for k,v,... in explist do body end` 布局按 [02](./02-bytecode-isa.md) §6:
`R(A)`=迭代函数、`R(A+1)`=状态、`R(A+2)`=控制变量,循环变量从 `R(A+3)` 起。

```
base := fs.freereg
adjust(explist → 3)               ; 迭代器三元组落 R(base..base+2)("(for generator)/(state)/(control)")
                                  ; 三个匿名内部局部
reserveRegs(nNames)               ; R(base+3 ..) 给可见循环变量 k,v,...
prep := emit JMP → (loop)         ; 先跳到 TFORLOOP(进循环先测一次)
enterBlock(isLoop=true)
  registerLocals(k,v,...) → R(base+3..)
  block body
leaveBlock()
patchToHere(prep)                 ; JMP 落到下面的 TFORLOOP
loop: emit TFORLOOP base, C=nNames
      emit JMP → body 起点(回边)
```
`TFORLOOP A C`:调 `R(A)(R(A+1),R(A+2))`,结果落 `R(A+3..A+2+C)`;若首值非 nil 则
`R(A+2):=R(A+3)`(更新控制变量)并由其后 JMP 回到 body,否则 `pc++` 跳过回边、退出(语义见
[02](./02-bytecode-isa.md) §4 行 33)。`TFORLOOP` 同样后随一条 JMP(回边),满足"成对"风格。

### 6.7 break

`break` = 一条无条件 `JMP`,链入**最内层可 break 块**(`bl.isLoop`)的 `breakList`:

```go
func (fs *funcState) codegenBreak() {
    bl := fs.innerLoopBlock()                  // 向上找最近 isLoop 块
    if bl == nil { error("no loop to break") }  // 对齐 5.1:'break' outside a loop(parser 也可早查)
    if bl.hasUpval { /* break 跨越捕获作用域,需在跳前 CLOSE,见 5.1 leaveblock 逻辑 */ }
    fs.concat(&bl.breakList, fs.jump())         // 把这条 JMP 接入 break 链
}
```
break 链在 `leaveBlock` 时 `patchToHere`(§6.1),统一落到循环块之后。Lua 5.1 要求 `break`
是 block 的最后一句(parser 层 §4.2 已限定),故无需处理 break 后的死代码。

---

## 7. 函数调用与方法调用、多返回值传播

### 7.1 普通调用 `f(a, b, ...)`

```
fnReg := fs.freereg
exp2NextReg(f)                    ; 函数落 R(fnReg)
adjust(args → 可变 or 固定)        ; 参数落 R(fnReg+1 ..);末位多值则 B=0
emit CALL A=fnReg B=(nargs+1 或 0) C=(待定,由上下文定取值数)
e.k = ECall ; e.info = CALL 的 pc
freereg = fnReg + 1               ; 调用结果默认占 R(fnReg) 起 1 个(单值上下文)
```
- `B`:参数个数+1;若末参是多值源(`Call`/`Vararg`),`B=0` 表示"参数到 top"(配合前一条
  产生多值的指令),对应 [02](./02-bytecode-isa.md) §3。
- `C`:返回值个数+1,**由调用方上下文回填**:作语句 → `C=1`(0 返回值);作表达式取 1 值 →
  `C=2`;作 explist 末位取多值 → `C=0`(到 top)。这正是 `ECall` 延迟物化的意义。

### 7.2 方法调用 `obj:m(args)` → SELF

用 [02](./02-bytecode-isa.md) §4 的 `SELF` 一条指令同时取方法和摆放 self:

```
baseReg := fs.freereg
exp2NextReg(obj)                  ; R(baseReg) = obj(临时)
emit SELF A=baseReg B=baseReg C=rk("m")
   ; 语义:R(baseReg+1) := R(baseReg)(self);R(baseReg) := R(baseReg)["m"](方法)
reserveRegs(1)                    ; SELF 额外占了 R(baseReg+1)
adjust(args → ...)                ; 实参从 R(baseReg+2) 起(self 已在 +1)
emit CALL A=baseReg B=(1+1+nargs 或 0) C=...
```
`SELF` 把 `obj` 既作 receiver(自动成为第 1 实参 self)又取出方法,省一条 GETTABLE + 一条
MOVE。方法名 `"m"` 走 RK 常量(`rk("m")`)。

### 7.3 多返回值 / vararg 的 B/C=0 传播

三处"末位多值"统一规约(§6.2 adjust):
- **return f(x)**:`RETURN A=fnReg B=0`(返回值到 top)——但若是尾位置,优先 TAILCALL(§9.4)。
- **t = {f()}**:table 构造末位 `f()` 用 `SETLIST B=0`(数组到 top,§8.1)。
- **g(f())**:`g` 的实参末位 `f()` 用 `CALL ... B=0`。
- **a, b = f()**:LHS 2 个,RHS 末位 `f()` 取 2 值 → `CALL C=3`。

`VARARG A B`:`...` 在 explist 末位时 `B=0`(全部 vararg 到 top);取固定 n 个时 `B=n+1`
(语义见 [02](./02-bytecode-isa.md) §4 行 37)。`...` 仅在 `IsVararg` 函数体内合法,否则 parser
报 `cannot use '...' outside a vararg function`。

---

## 8. 表构造与闭包

### 8.1 表构造 `{ ... }`(NEWTABLE + SETLIST + SETTABLE)

```
tReg := fs.freereg
np := emit NEWTABLE A=tReg B=?(数组项数估算) C=?(哈希项数估算)  ; B/C 是 int2fb 浮点编码(见下)
reserveRegs(1)                    ; 表占 R(tReg)
// —— 数组部分(AKeys):每攒满 FPF=50 个 flush 一次 ——
for 每个数组值 v(按序):
    exp2NextReg(v)                ; 值落 R(tReg+1+计数)
    if 攒满 50 { emit SETLIST tReg, B=50, C=批号; freereg=tReg+1 }
末批(末位多值则 B=0 到 top,否则 B=剩余数): emit SETLIST tReg, B, C=末批号
// —— 哈希部分(HKeys/HVals):逐字段 SETTABLE,不占用连续寄存器水位 ——
for (k,v) in zip(HKeys,HVals):
    rkK := exp2RK(k) ; rvV := exp2RK(v)
    emit SETTABLE tReg, RK=rkK, RK=rvV
    freereg 回到 tReg+1            ; 哈希字段是即用即弃的临时
```
- `SETLIST A B C`:`R(A)[(C-1)*FPF + i] := R(A+i)`(i=1..B),FPF=50,与
  [02](./02-bytecode-isa.md) §4 行 34 + §4 注「FPF=50」一致。`C` 是批号(第几个 50);`B=0`
  表示数组到 top(末位多值);批号过大(C 放不下 9-bit)时 `C=0` 并用**下一条指令**承载大批号
  (对齐 Lua 5.1 `SETLIST` 的 `C==0` 取下一指令为批号约定,[02](./02-bytecode-isa.md) §4 行
  34 已注明)。
- `NEWTABLE` 的 `B`(数组预分配)、`C`(哈希预分配)用 Lua 的 `int2fb` 把容量近似编码进
  9-bit(`int2fb`/`fb2int`,[02](./02-bytecode-isa.md) §10 缺口已标注公式落 `internal/bytecode`
  helper)。codegen 据 AST 里数组项数、哈希项数估算后编码。

### 8.2 函数定义与闭包(CLOSURE + 后随 upvalue 伪指令)

`FuncExpr` → 新建子 `funcState`,递归 codegen 其 Body 产出子 `Proto`,注册得 `ProtoID`,在
外层发 `CLOSURE`:

```
childProtoIdx := len(parent.proto.Protos)        ; 在 Protos 表登记子 ProtoID
parent.proto.Protos = append(..., childProtoID)
emit CLOSURE A=freereg Bx=childProtoIdx
reserveRegs(1)
// ★ 紧随 nupvals 条伪指令,逐个描述子函数每个 upvalue 从何捕获(见 02 §4 行 36)
for each uv in child.upvals:
    if uv.inStack { emit MOVE 0, uv.idx }         ; 捕获外层【局部寄存器】→ 伪 MOVE,B=外层寄存器号
    else          { emit GETUPVAL 0, uv.idx }     ; 捕获外层【upvalue】→ 伪 GETUPVAL,B=外层 upvalue 索引
```
这些**伪指令**(A 字段无意义,固定 0)不被解释器当普通指令执行,而是 `CLOSURE` 执行时**顺序
读取**它们来填充新闭包的 upvalue 数组([02](./02-bytecode-isa.md) §4 行 36「随后 nupvals 条
伪指令」、[01](./01-value-object-model.md) §5.3 Closure 的 `upvalRef[nupvals]`)。

`function funcname() ... end`(`FuncStmt`)= 先 codegen `FuncExpr` 得闭包寄存器,再按
`Target`(NameExpr→SETGLOBAL/SETUPVAL/局部 MOVE;IndexExpr→SETTABLE)赋值,见 §5 赋值模式。
方法形式 `a:m()` 由 parser 注入隐式 `self` 形参(§3.3)。

### 8.3 UpvalDesc 的 inStack / idx 生成

`Proto.UpvalDescs`([01](./01-value-object-model.md) §5.7)每项描述本函数某 upvalue 的来源:

```go
type upvalDesc struct {
    name    string  // 调试名
    inStack bool    // true: 捕获【直接外层函数的局部寄存器】;false: 捕获外层函数的某个 upvalue
    idx     uint8   // inStack=true → 外层寄存器号;false → 外层 upvalue 索引
}
```
`inStack=true` 对应 §8.2 发 `MOVE` 伪指令;`inStack=false` 对应 `GETUPVAL` 伪指令。生成逻辑
见 §8.4。

### 8.4 upvalue 解析(词法作用域链查找)

解析 `NameExpr{name}` 时(`singlevar`/`searchvar` 逻辑),从当前函数沿 `prev` 链向外找:

```
resolveName(fs, name):
  1. 在 fs 的活跃局部中找 → ELocal(info=寄存器号)                 // 本函数局部
  2. 否则在 fs.upvals 找同名 → EUpval(info=upvalue 索引)          // 已捕获过
  3. 否则递归 resolveName(fs.prev, name):
       - 返回 ELocal(r)  ⇒ 在 fs 新增 upvalDesc{inStack:true, idx:r};
                           并把【外层 fs.prev 的那个局部所在 block.hasUpval = true】(供 CLOSE)
       - 返回 EUpval(u)  ⇒ 在 fs 新增 upvalDesc{inStack:false, idx:u}
       - 返回 EGlobal    ⇒ 本函数也作 EGlobal(全局穿透)
     新增后返回 EUpval(新索引)
  4. 顶层仍未找到 → EGlobal(info = K("name")) ⇒ GETGLOBAL/SETGLOBAL(经 _ENV/globals)
```
要点:
- **每函数 upvalue 表去重**:同名 upvalue 在一个函数内只登记一次(步骤 2 命中复用)。
- **嵌套穿透**:深层函数引用更外层局部时,中间每一层都登记一个 `inStack=false` 的 upvalue
  (链式转发),只有最贴近"局部定义层"的那一层用 `inStack=true`。这与 Lua 5.1 一致。
- **触发 hasUpval**:一旦某外层局部被内层捕获,标记其所在 block `hasUpval=true`,使该 block
  退出时发 `CLOSE`(§6.1),把开放 upvalue 关闭为自持值。

---

## 9. 编译期错误与资源上限

codegen 阶段的错误均为**资源/结构类**(语法错误已在 parse 阶段报);措辞对齐 Lua 5.1
`lcode.c`/`lparser.c` 以便复用 conformance 套与差分:

| 触发条件 | 措辞(对齐 5.1) | 检查点 |
|---|---|---|
| 局部变量(含匿名内部槽)超 200 | `too many local variables` | `registerLocal`(`nactvar` > `LUAI_MAXVARS`=200) |
| upvalue 超 60 | `too many upvalues` | `resolveName` 新增 upvalue 时(> `LUAI_MAXUPVALUES`=60) |
| 寄存器水位 > 250 | `function or expression too complex`(寄存器溢出) | `checkStack`(`freereg` > `MAXSTACK`=250,对齐 A 字段 8-bit 上限,[02](./02-bytecode-isa.md) §2) |
| 常量池超 2^18 | `constant table overflow` | `addConst`(超 `MAXARG_Bx`,[02](./02-bytecode-isa.md) §2 Bx=18-bit) |
| 跳转偏移越界 | `control structure too long` | `fixJump`(`|sBx|` > 131071,[02](./02-bytecode-isa.md) §2 sBx) |
| 嵌套函数 / Protos 过多 | `too many functions`(子 Proto 超 Bx) | CLOSURE 登记子 Proto 时 |
| `break` 不在循环内 | `no loop to break`(5.1:`'break' outside a loop at ...`) | §6.7(parser 也可早查) |
| `...` 不在 vararg 函数 | `cannot use '...' outside a vararg function` | parser/codegen VarargExpr |

注:`MAXSTACK=250` 而非 255,留 fixstack 余量(与 [02](./02-bytecode-isa.md) §2「`MaxStack ≤
250`」一致)。所有上限值落 `internal/bytecode` 常量,codegen 引用,单一事实源。

### 9.4 尾调用识别 `return f(...)` → TAILCALL

`ReturnStmt` codegen:若 `Exprs` 恰为**单个**`CallExpr`/`MethodCallExpr`(`return f(args)`,无
其它返回值、无括号包裹强制单值),则把该调用发为 `TAILCALL` 而非 `CALL`,复用当前帧(栈不
增长,[02](./02-bytecode-isa.md) §4 行 29):

```
if len(Exprs)==1 && Exprs[0] is (Method)Call && 非 '(f())' 强制单值形式:
    codegen 调用,但把最终 CALL 改发 TAILCALL A=fnReg B=(nargs+1 或 0)
    emit RETURN A=fnReg B=0          ; TAILCALL 后仍跟一条 RETURN(到 top),供非尾调用兜底语义
else:
    adjust(Exprs → 可变) ; emit RETURN A=base B=(nret+1 或 0)
```
识别规则严格对齐 Lua 5.1 `retstat`:仅"裸调用作唯一返回表达式"才尾调用;`return (f())` 因括号
强制单值、`return f(), 1` 因有附加值,都**不**尾调用。`return`(无表达式)→ `RETURN A=0 B=1`。
每个函数末尾若无显式 return,codegen 补一条 `RETURN A=0 B=1`(隐式 return 无值,见
[02](./02-bytecode-isa.md) §8 末行)。

---

## 10. 端到端示例(与 [02](./02-bytecode-isa.md) §8 自洽)

复用 [02](./02-bytecode-isa.md) §8 的求和函数,展示 parse→AST→codegen 全流程,**寄存器分配与
02 文档逐字节一致**作为自洽证据。

源:
```lua
local function f(n)
  local s = 0
  for i=1,n do s = s + i*i end
  return s
end
```

### 10.1 AST(parse 产出)

```
Block[
  LocalFuncStmt{ Name:"f", Fn:FuncExpr{
      Params:["n"], IsVararg:false, Body:Block[
        LocalStmt{ Names:["s"], Exprs:[NumberExpr{0}] },
        NumForStmt{ Var:"i", Init:NumberExpr{1}, Limit:NameExpr{"n"}, Step:nil,
          Body:Block[
            AssignStmt{ Targets:[NameExpr{"s"}],
                        Exprs:[ BinExpr{OpAdd, NameExpr{"s"},
                                        BinExpr{OpMul, NameExpr{"i"}, NameExpr{"i"}} ] }
          ]},
        ReturnStmt{ Exprs:[NameExpr{"s"}] }
      ]}}
]
```

### 10.2 codegen(子 Proto `f` 的寄存器分配过程)

| 步骤 | 动作 | freereg / 寄存器认领 |
|---|---|---|
| 进入 `f`,形参 `n` | `registerLocal("n")` | R0=n,nactvar=1,freereg=1 |
| `local s = 0` | RHS `0` 是 EKNum;`exp2NextReg` → `LOADK R1 K0(0)`;`registerLocal("s")` | R1=s,nactvar=2,freereg=2 |
| `for i=1,n` 三槽 | init=1:`LOADK R2 K1(1)`;limit=n:`MOVE R3 R0`;step 缺省:`LOADK R4 K1(1)`;预留 R5 给 i | R2/R3/R4 内部槽,R5=i 预留,freereg=6 |
| `FORPREP` | `FORPREP R2 → L1` | nactvar 进 for 块后含 i=5 |
| body:`i*i` | `MUL R6 R5 R5`(R5=i,临时落 R6) | freereg 暂到 7,算完归还 |
| body:`s+tmp` | `ADD R1 R1 R6`(s=R1,结果回 R1) | R6 归还,freereg 回 6 |
| `FORLOOP` | `FORLOOP R2 → L0`(回边) | |
| `return s` | `RETURN R1 2`(B=2 ⇒ 返回 1 值 R1) | |
| 隐式 return | `RETURN R0 1`(B=1 ⇒ 0 值) | |

### 10.3 产出字节码(与 [02](./02-bytecode-isa.md) §8 完全一致)

```
; Proto f: NumParams=1, IsVararg=false, MaxStack=7
; Consts: K0=0.0, K1=1.0
LOADK     R1  K0          ; s = 0
LOADK     R2  K1          ; (for index) = 1
MOVE      R3  R0          ; (for limit) = n
LOADK     R4  K1          ; (for step) = 1
FORPREP   R2  -> L1
L0: MUL   R6  R5  R5      ; tmp = i*i      (i = R(A+3) = R5)
    ADD   R1  R1  R6      ; s = s + tmp
L1: FORLOOP R2  -> L0     ; i+=1; if i<=n goto L0(热点回边)
RETURN    R1  2           ; return s
RETURN    R0  1           ; 隐式 return
```
外层主 chunk 再发 `CLOSURE`+无 upvalue 伪指令(`f` 不捕获外层局部)把 `f` 落到主 chunk 的某
局部寄存器(因 `local function f`)。`MaxStack=7`(用到 R0..R6)与 §10.2 水位线一致。**寄存器
号、常量号、跳转标签与 02 文档 §8 逐项相同**——这正是 §1.1「寄存器分配同构」承诺的兑现,也是
[12-testing-difftest](./12-testing-difftest.md) 逐字节差分得以成立的前提。

---

## 11. 常量去重与字符串 intern

`addConst(v Value) int`:维护一张 `map[constKey]int`(`funcState.proto` 旁,Lua 5.1 用
`FuncState.h` 一个 Lua 表去重),相同常量复用同一 K 槽:
- 数字:以 `float64` bits 为 key(注意 `+0.0`/`-0.0` 视情况;NaN 折叠后规范化为 canonNaN,
  [01](./01-value-object-model.md) §3.4)。
- 字符串字面量:codegen 时**intern 到 arena**(经 `object`/`arena` 的 string interning,
  [01](./01-value-object-model.md) §5.1),得 GCRef,以 GCRef 为去重 key;该 GCRef 进
  `Proto.Consts` 即成 GC 根([01](./01-value-object-model.md) §1)。相同字面量 → 同一 GCRef →
  同一 K 槽。
- `nil`/`bool` 一般不入常量池(用 `LOADNIL`/`LOADBOOL`),但 `addConst` 允许容纳(
  [02](./02-bytecode-isa.md) §5)。

去重保证:① RK 槽位紧凑(更易 < 256 命中 RK 快路径);② 字符串相等退化为 GCRef 相等
([01](./01-value-object-model.md) §7 不变式 3)。

---

## 12. 不变式清单(实现与差分须守)

1. **寄存器分配同构**:freereg 水位线、局部绑定连续低位、临时落 freereg、`exp2RK` 折叠——产出
   与 Lua 5.1 luac **逐字节可比**(§1.1、§10)。
2. **块边界 `freereg==nactvar`**:进出任何 block 都成立(§5.1、§6.1)。
3. **比较成对**:`EQ/LT/LE`/`TEST`/`TESTSET` 后**必跟 JMP**,codegen 保证(§5.5/§5.6,
   [02](./02-bytecode-isa.md) §9 不变式 3)。
4. **多值边界**:`CALL/RETURN/VARARG/SETLIST` 的 `B=0`/`C=0` 仅由 §6.2 adjust 在"末位多值"
   时产生([02](./02-bytecode-isa.md) §9 不变式 4)。
5. **CLOSURE 后伪指令数 == 子 Proto nupvals**:逐个 `MOVE`/`GETUPVAL` 描述捕获(§8.2)。
6. **MaxStack 静态算定**:= 编译期 freereg 峰值,写入 `Proto.MaxStack`(§5.3,
   [01](./01-value-object-model.md) §5.7)。
7. **语义对齐 5.1**:任何疑义以 Lua 5.1 参考实现为准,由差分测试钉死(
   [12-testing-difftest](./12-testing-difftest.md))。

---

## 13. 文档缺口 / 待决(记入 memory/doc-gaps)

- ~~token 流契约依赖 03 未定稿~~:**已关闭**——[03-frontend-lexer](./03-frontend-lexer.md) 已定稿:lexer 产带行号 token、数字经 `value.NumberValue` 转 `float64`、字符串字面量为 Go string(intern 留 codegen,§11)、长注释已剥离;LL(2) 前瞻定为 **lexer 只供 `Next()`,parser 自缓存一格 `ahead`**(03 §2,与本文 §4.1 精确咬合)。
- ~~局部变量调试表的精确布局~~:**已关闭**——[01](./01-value-object-model.md) §5.7 已增补 `LocVars []LocalVar`(Name + StartPC/EndPC,§5.9 的 `removeVars` 闭合后写入);upvalue 名复用 `UpvalDescs.name`(§8.3),不单列 `UpvalNames`。
- **`int2fb`/`fb2int` 公式**:NEWTABLE 的 B/C 容量编码、SETLIST 大批号(C=0 取下一指令)沿用
  Lua 5.1,公式落 `internal/bytecode` helper(与 [02](./02-bytecode-isa.md) §10 同一缺口)。
- **常量折叠的边界口径**:`1/0`、`0/0`、`2^63` 等编译期折叠是否与运行期解释**逐字节同结果**
  (尤其 NaN/Inf 的 boxing 表示),需 [12-testing-difftest](./12-testing-difftest.md) 给口径;
  保守做法:codegen 折叠走与解释器**同一** `value.NumberValue`(含 canonicalize)即天然一致。
- **`pairs`/表构造顺序的可观察性**与本文无关但相邻:数组/哈希字段发射顺序影响 rehash 行为,
  最终是否要求与 gopher-lua 逐字节一致是验收口径问题(见 [01](./01-value-object-model.md) §8
  与 [12](./12-testing-difftest.md))。
- **parser 错误恢复策略**:P1 是"首错即停"还是"错误恢复后续报多条"未定;建议 P1 首错即停
  (实现简单,差分只需正例),错误恢复留后续。

---

相关:[01-value-object-model](./01-value-object-model.md) · [02-bytecode-isa](./02-bytecode-isa.md) ·
[03-frontend-lexer](./03-frontend-lexer.md) · [05-interpreter-loop](./05-interpreter-loop.md) ·
[12-testing-difftest](./12-testing-difftest.md) · [../architecture](../architecture.md) ·
[../../../llmdoc/architecture/value-representation](../../../llmdoc/architecture/value-representation.md)
