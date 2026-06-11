# P1 前端:词法分析器(lexer)

> 状态:**设计阶段,可实现深度**。本文定义前端第一段——**lexer**(源码字节流 → 带行号的
> token 流),把原始 chunk 切成下游 parser 直接消费的 token。下游契约是
> [04-frontend-parser-codegen](./04-frontend-parser-codegen.md)(其 §4.1 的 `Parser{tok,
> ahead,hasAhead}` 与 §3.4 运算符枚举就是本文 token 接口的直接调用方;§13 第一条缺口
> 「token 字段 / 种类枚举 / 字面量载荷 / LL(2) 前瞻归属」由本文兑现)。数字字面量经
> [01-value-object-model](./01-value-object-model.md) §3.4 `value.NumberValue`(含 NaN
> 规范化)产出,与运行期一致;字符串字面量本阶段只产 Go `string`,**intern 留给 codegen**
> ([04](./04-frontend-parser-codegen.md) §11、[01](./01-value-object-model.md) §5.1)。
> 语言面限定 Lua 5.1(`docs/design/roadmap.md` (§6),**不做** 5.2+ 的 goto / hex 浮点 /
> `\x` / `\u` / `\z`)。

对应 Go 包:`internal/frontend/token`(token 种类与字面量,纯定义)、`internal/frontend/lex`
(lexer)。包布局见 [../architecture](../architecture.md) §1;依赖方向 `lex → token`
(+ `value` 仅用于数字字面量转换),无环(`architecture.md` §3)。

---

## 1. 包职责与数据流

### 1.1 两个包的分工

| 包 | 输入 | 输出 | 不做 |
|---|---|---|---|
| `internal/frontend/token` | — | `Token` struct、`Kind` 枚举、关键字表、`Kind.String()` 调试名(纯数据/纯定义) | 不含扫描逻辑 |
| `internal/frontend/lex` | 源码 `[]byte`(或 `string`)+ chunk 名 | **逐个** `token.Token`(经 `Next()` 拉取)+ 词法错误 | 不建 AST、不 intern 字符串、不查全局/局部(那是 codegen 职责) |

`token` 是叶子包,**无任何依赖**(连 `value` 都不依赖:数字字面量载荷是裸 `float64`,转换在
`lex` 里完成)。`lex` 依赖 `token`,并依赖 `value` 仅为调用
[01](./01-value-object-model.md) §3.4 的 `value.NumberValue`(把解析出的 `float64`
规范化——见 §5.4)。这条最小依赖与 `architecture.md` §3「`value` 不依赖上层、前端不被解释器
绑死」一致。

> 设计注记:也可让 `lex` 完全不依赖 `value`,产出裸 `float64`,把 `NumberValue` 规范化推迟
> 到 codegen 的 `addConst`。两种口径**结果等价**(都是「数字字面量最终走同一 `NumberValue`」)。
> 本文选**在 lexer 处即调 `NumberValue`** 装进 token 载荷(§2、§5.4),理由:① token 载荷
> 自此就是「值世界合法的规范化 double」,parser/codegen 无需再操心 NaN 渗入;② 与 04 §13
> 「数字已转 float64」的契约措辞最贴合。若实现期发现 `lex→value` 依赖不便,允许退化为
> 「lexer 产裸 float64 + codegen 处 `NumberValue`」,**载荷类型不变**(仍是 `float64`),
> 仅规范化时机前后移,差分无可观察差异——记入 §13 缺口。

### 1.2 pull 式接口 + parser 自缓存一个 lookahead(关键决策)

**决策:lexer 提供 `Next() Token` 的 pull 式接口,一次产一个 token;LL(2) 前瞻由 parser
自缓存(`Parser.ahead`/`hasAhead`),lexer 不提供 `Peek`。**

```go
type Lexer struct {
    src    []byte    // 整段源码(已读入内存;Compile 是一次性的,见 04 §1.1)
    pos    int       // 当前读取偏移(下一个待读字节下标)
    line   int32     // 当前行号(从 1 起,见 §9)
    source string    // chunk 名(错误定位用,如 "@file.lua" / "=stdin" / 字符串源)
    buf    []byte    // 复用的字符串/数字累积缓冲(避免每 token 分配)
}

func New(src []byte, source string) *Lexer
func (lx *Lexer) Next() (token.Token, error)   // 拉下一个 token;到结尾产 EOF;词法错误即返回
```

**为什么 pull 式而非「一次性产 `[]Token`」**:

1. **与 04 §4.1 的 `Parser` 结构精确咬合**。04 已把 parser 设计成持有 `tok`(当前 token)
   + `ahead`(预读一格)+ `hasAhead`(预读是否有效)。这正是 Lua 官方 `llex.c` 的
   `LexState{ t, lookahead }` 双 token 模型:`t` 是当前、`lookahead` 是预读。parser 自己
   维护这两格,**只需要 lexer 能「再给我下一个」**——即 `Next()`,不需要 lexer 暴露
   `Peek`。把前瞻缓存放在 parser 侧,职责更清晰:lexer 是无状态记忆的「流」,前瞻策略是
   parser 的语法决策细节(04 §4.4 赋值 vs 调用消歧才需要这一格)。
2. **避免重复缓冲与所有权混淆**。若 lexer 自带 `Peek`,则 lexer 与 parser 各存一份「下一个
   token」,两份真相易错;pull + parser 单缓存只有一份。
3. **可流式、低峰值内存**(虽 P1 不追编译速度,但形状更干净)。一次性 `[]Token` 要为整个
   chunk 物化全部 token;pull 式峰值仅 2 个 token(`tok`+`ahead`)。
4. **错误即时短路**:`Next()` 遇非法 token 立刻返回 error(§11),parser「首错即停」
   (04 §13 末条:P1 错误恢复留后续),无需先把整段扫完。

**parser 侧的驱动代码**(回填 04 §4.1,供对照,非本包代码):

```go
// parser 用这两个原语驱动 lexer(都在 internal/frontend/parse)
func (p *Parser) next() {            // 前进一格:ahead 有则取之,否则向 lexer 拉
    if p.hasAhead {
        p.tok, p.hasAhead = p.ahead, false
    } else {
        p.tok = p.mustNext()         // 包装 lx.Next();错误转 parser error
    }
}
func (p *Parser) peek() token.Token { // 看下一格(LL(2));只在 §4.4 等少数产生式用
    if !p.hasAhead {
        p.ahead, p.hasAhead = p.mustNext(), true
    }
    return p.ahead
}
```

> **对 04 §13 第一条缺口的正式回答(归属)**:**LL(2) 前瞻归 parser 自缓存**(上面的
> `ahead`/`hasAhead`),lexer **只**提供 `Next()`。这与 Lua 官方 `luaX_next` /
> `luaX_lookahead` 的分工同构(`luaX_lookahead` 也是把预读存进 `LexState.lookahead`,而非让
> 扫描器多吐)。详见 §12。

数据流总览(承接 `architecture.md` §3):

```
            lex 包                       parse 包(下游)
 []byte ──► Lexer.Next() ──逐 token──► Parser{ tok, ahead, hasAhead } ──► ast.Block
 (source)     §3..§10        §2 Token        04 §4.1
```

---

## 2. `token.Token` 结构

每个 token 是「**种类 + 行号 + 可选字面量载荷**」。Lua 5.1 字面量只有三类承载值(数字 /
字符串 / 标识符名),其余 token(关键字、符号、EOF)只靠 `Kind` 区分,无载荷。

```go
package token

type Token struct {
    Kind Kind     // 种类(见 §3 全枚举)
    Line int32    // 该 token 起始源行(从 1 起;多行字符串取【起始】行,见 §6.3)

    // —— 字面量载荷(仅对应 Kind 时有效)——
    Num float64   // Kind==Number:已转 float64 且已 NaN 规范化(§5.4、01 §3.4)
    Str string    // Kind==String:解码后的字节内容(转义已展开 / 长串已剥壳)
    Name string   // Kind==Name:标识符原文(关键字已在扫描时归入各自 Kind,不进这里)
}
```

设计注记与契约要点:

- **载荷形态(回答 04 §13)**:
  - 数字 → `Num float64`(**已转 float64**:十进制/十六进制/指数都已归约;**已规范化**:
    经 `value.NumberValue`,NaN 恒为 canonNaN,见 §5.4)。parser 直接塞进
    `ast.NumberExpr{Val: tok.Num}`(04 §3.2)。
  - 字符串 → `Str string`(**已解码**:短串转义已展开、长串首换行已丢弃且无转义;**未
    intern**)。parser 直接塞进 `ast.StringExpr{Val: tok.Str}`(04 §3.2 明确 `StringExpr`
    载荷是 Go `string`,**codegen 时才 intern**,见 [01](./01-value-object-model.md) §5.1、
    04 §11)。⇒ **lexer 产 Go `string`,arena intern 不在本阶段**——这是本文与 01/04 的硬
    自洽点。
  - 标识符 → `Name string`(原文;关键字**不**走这里,见 §4.2)。
- **`Str` 用 `string` 还是 `[]byte`**:Lua 字符串是**任意字节序列**(可含 `\0`),Go `string`
  恰好也是不可变字节序列、可含 `\0`、可作 map key(codegen `addConst` 去重要用),故选
  `string`。`[]byte` 会引入可变性与不可作 key 的麻烦。短串解码时在 `lx.buf`([]byte)累积,
  最后 `string(buf)` 一次拷出。
- **`Line` 是起始行**:多行长字符串/长注释跨行时,token 行号记**开始**那一行(对齐 Lua 5.1
  `LexState.linenumber` 在 token 起始处的快照),与 04 「带行号的 token」契约一致;行内累加
  见 §9。
- **零值即 EOF 友好**:`Kind` 的 `EOF` 不必是 0(见 §3 枚举顺序),但 `Next()` 到结尾稳定
  反复产 `EOF`(parser 可安全多次 `peek`)。
- **不含 `End`/列号**:P1 token 只带行号(够 04 的 `Line int32` 与错误定位)。列号 / 起止
  偏移留作 §13 缺口(未来 source map / 更精细诊断再加),保持 token 紧凑。

> 内存注记:`Token` 是值类型(by value 在 parser 的 `tok`/`ahead` 间拷贝)。三个载荷字段中
> 至多一个有效,略有空间浪费,但 token 不入 arena、不长期持有(parser 用完即弃),换取「无
> 指针、无分配、拷贝即 memmove」的简单性,符合项目「值即数据」的基调。

---

## 3. token 种类全枚举(`Kind`)

Lua 5.1 的终结符集是**封闭**的。下面是**全部** `Kind`:21 个关键字 + 全部符号/算符 + 3 类
字面量 + EOF。**不含** 5.2+ 的 `goto`(那是关键字)、`::`(label)、`//`(整除)、位运算符
`& | ~ << >>`、`~`(5.3 一元位非)——这些 `roadmap.md` (§6) 已排除,本枚举里**不出现**。

```go
type Kind uint8

const (
    // —— 特殊 ——
    EOF Kind = iota   // 输入结束

    // —— 字面量类(带载荷)——
    Number            // 数字字面量    → Token.Num
    String            // 字符串字面量  → Token.Str
    Name              // 标识符        → Token.Name

    // —— 关键字(21 个,Lua 5.1)——
    KwAnd; KwBreak; KwDo; KwElse; KwElseif; KwEnd; KwFalse; KwFor
    KwFunction; KwIf; KwIn; KwLocal; KwNil; KwNot; KwOr; KwRepeat
    KwReturn; KwThen; KwTrue; KwUntil; KwWhile

    // —— 符号 / 算符 ——
    Plus      // +
    Minus     // -
    Star      // *
    Slash     // /
    Percent   // %
    Caret     // ^
    Hash      // #
    Eq        // ==
    Ne        // ~=
    Le        // <=
    Ge        // >=
    Lt        // <
    Gt        // >
    Assign    // =
    LParen    // (
    RParen    // )
    LBrace    // {
    RBrace    // }
    LBracket  // [
    RBracket  // ]
    Semi      // ;
    Colon     // :
    Comma     // ,
    Dot       // .
    Concat    // ..
    Ellipsis  // ...
)
```

### 3.1 符号/算符对照表

| `Kind` | 拼写 | 用途(下游) | 多字符消歧 |
|---|---|---|---|
| `Plus` `Minus` `Star` `Slash` `Percent` `Caret` | `+ - * / % ^` | 算术二元(`-` 兼一元负);`^` 右结合(04 §4.3) | 单字符 |
| `Hash` | `#` | 一元长度(04 §3.4 `OpLen`) | 单字符 |
| `Eq` `Ne` `Le` `Ge` `Lt` `Gt` | `== ~= <= >= < >` | 关系(04 §3.4 `OpEq..OpGe`) | `=`→`==`、`~`→`~=`、`<`→`<=`、`>`→`>=`(看下一字符是否 `=`) |
| `Assign` | `=` | 赋值(04 §4.4) | `=` 后非 `=` 才是 `Assign` |
| `LParen` `RParen` `LBrace` `RBrace` `LBracket` `RBracket` | `( ) { } [ ]` | 分组 / table / 索引 / 长括号起手 | `[` 后若是 `[` 或 `=` 可能是**长字符串**(§6.2),由扫描器优先试长括号 |
| `Semi` `Colon` `Comma` `Dot` `Concat` `Ellipsis` | `; : , . .. ...` | 语句分隔 / 方法 / 列表 / 索引 / 连接 / vararg | `.` 后看是否再 `.`(→`..`),再看是否第三个 `.`(→`...`);**数字开头的 `.5` 在数字扫描里处理**(§5),不与 `Dot` 冲突 |

### 3.2 关键字表

关键字**先扫成标识符,再查表**(§4.2)。表是 `token` 包的不可变 `map[string]Kind`:

| 词 | `Kind` | 词 | `Kind` | 词 | `Kind` |
|---|---|---|---|---|---|
| `and` | `KwAnd` | `function` | `KwFunction` | `repeat` | `KwRepeat` |
| `break` | `KwBreak` | `if` | `KwIf` | `return` | `KwReturn` |
| `do` | `KwDo` | `in` | `KwIn` | `then` | `KwThen` |
| `else` | `KwElse` | `local` | `KwLocal` | `true` | `KwTrue` |
| `elseif` | `KwElseif` | `nil` | `KwNil` | `until` | `KwUntil` |
| `end` | `KwEnd` | `not` | `KwNot` | `while` | `KwWhile` |
| `false` | `KwFalse` | `or` | `KwOr` | | |
| `for` | `KwFor` | | | | |

共 **21 个**(Lua 5.1 `luaX_tokens` 前 21 个保留字)。`nil`/`true`/`false` 在 Lua 5.1 是
**关键字**(不是普通标识符常量),parser 在 `parseSimpleExpr`(04 §4.2)按 `KwNil/KwTrue/
KwFalse` 直接产 `NilExpr/TrueExpr/FalseExpr`(04 §3.2)。

> **明确排除(5.2+,本项目不收)**:`goto`。它在 Lua 5.2 才成为保留字;本枚举无 `KwGoto`、
> 无 `::` 标签 token。若源码出现 `goto`,本 lexer 把它当**普通标识符**(`Name="goto"`)——
> 这正是 Lua 5.1 的行为(5.1 里 `goto` 不是保留字,可作变量名)。**不报错**,交由后续语义
> 自然处理。记入 §13(与差分基准 5.1 一致即可)。

---

## 4. 标识符与关键字

### 4.1 标识符文法

```
Name  ::=  [A-Za-z_] [A-Za-z0-9_]*
```

- 首字符:ASCII 字母或下划线;后续:ASCII 字母 / 数字 / 下划线。
- 扫描:`isNameStart(c) = c=='_' || isAlpha(c)`;`isNameCont(c) = isNameStart(c) || isDigit(c)`。
  从 `isNameStart` 起,贪心吃 `isNameCont`,得子串 `s`。

```go
func (lx *Lexer) scanName() token.Token {
    start := lx.pos
    for lx.pos < len(lx.src) && isNameCont(lx.src[lx.pos]) { lx.pos++ }
    s := string(lx.src[start:lx.pos])
    if kw, ok := token.Keyword(s); ok {          // 查关键字表(§3.2)
        return token.Token{Kind: kw, Line: lx.line}
    }
    return token.Token{Kind: token.Name, Line: lx.line, Name: s}
}
```

### 4.2 关键字识别 = 先 Name 后查表

不为每个关键字写专门分支,而是**统一扫成标识符子串,再 `token.Keyword(s)` 查表**(上)。命中
则该 token 的 `Kind` 是对应关键字、**不带** `Name` 载荷;未命中才是 `Name`。这与 Lua 官方
`llex.c` 用保留字表 + 「先 read_name 再判 reserved」一致,且只一处扫描逻辑。

### 4.3 非 ASCII 标识符:定死 ASCII-only(记缺口)

Lua 5.1 的 `isalpha`/`isalnum` 取决于 **C locale**:在某些 locale 下高位字节(`>= 0x80`)可
能被当字母,允许「带重音的标识符」,但这是**实现/平台相关、不可移植**的行为。

> **决策:望舒 lexer 标识符严格 ASCII-only**(`isNameStart`/`isNameCont` 只认 `[A-Za-z0-9_]`)。
> 高位字节既不作标识符首/续字符,也不作合法符号——遇到落入「非法字符」错误(§11
> `unexpected symbol`)。**例外**:高位字节出现在**字符串字面量内**或**注释内**时按原始字节
> 透传/吞掉(字符串内容是任意字节,§6;注释整体丢弃,§7),不受此限。

理由:① **差分一致性优先**(`architecture.md` §4 不变式 2):locale 相关行为无法稳定对拍,
ASCII-only 是可复现的最小公约;② 纯 Go 无 `setlocale`,默认即「C locale 近似」,ASCII-only
与多数平台默认行为吻合;③ 真实 Lua 5.1 脚本几乎不依赖非 ASCII 标识符。**缺口**:若未来差分基
准(gopher-lua)在某些字节上与我们不一致,需在 [12-testing-difftest](./12-testing-difftest.md)
固定口径——记入 §13。

---

## 5. 数字字面量

Lua 5.1 数字**全部是 double**(无整数子类型,`roadmap.md` (§6) 已排除 5.3 整数)。词法上覆盖
四种写法,统一归约为一个 `float64`。

### 5.1 文法

```
Number ::= Hex | Decimal
Hex      ::= ('0x' | '0X') HexDigit+                       -- 仅【整数】十六进制(见 5.3)
Decimal  ::= Digit+ ('.' Digit*)? Exp?
           | '.' Digit+ Exp?                               -- 允许以小数点起头,如 .5
           | Digit+ Exp                                    -- 如 1e10
Exp      ::= ('e' | 'E') ('+' | '-')? Digit+
Digit    ::= [0-9]
HexDigit ::= [0-9a-fA-F]
```

要点:
- **小数点可在任意位置**:`3.`、`.5`、`3.14` 都合法;`.` 起头的数字由「`.` 后紧跟数字」触发
  数字扫描(否则 `.` 是 `Dot`/`Concat`/`Ellipsis`,§3.1)。
- **指数**:`e`/`E` 后可带可选 `+`/`-`,再跟**十进制**数字(指数部分始终十进制,即便尾数是
  hex 也不适用——因为 5.1 hex 根本无指数,见下)。

### 5.2 扫描策略(贪心累积 + strconv 解析)

```go
func (lx *Lexer) scanNumber() (token.Token, error) {
    start := lx.pos
    if lx.src[lx.pos] == '0' && lx.pos+1 < len(lx.src) &&
       (lx.src[lx.pos+1]|0x20) == 'x' {              // 0x / 0X
        lx.pos += 2
        for lx.pos < len(lx.src) && isHex(lx.src[lx.pos]) { lx.pos++ }
        // ★ 5.1:hex 到此为止;不接受 '.'、'p'、'P'(那是 5.2+ hex 浮点),见 5.3
    } else {
        for lx.pos < len(lx.src) && isDigit(lx.src[lx.pos]) { lx.pos++ }
        if lx.peekByte() == '.' { lx.pos++; for isDigit(...) { lx.pos++ } }
        if (lx.peekByte()|0x20) == 'e' {              // 指数
            lx.pos++
            if lx.peekByte()=='+' || lx.peekByte()=='-' { lx.pos++ }
            for isDigit(...) { lx.pos++ }              // 缺数字 ⇒ malformed(下)
        }
    }
    lit := string(lx.src[start:lx.pos])
    f, ok := parseLuaNumber(lit)                       // §5.4
    if !ok { return zero, lx.errf("malformed number near '%s'", lit) }
    return token.Token{Kind: token.Number, Line: lx.line, Num: f}, nil
}
```

**malformed 判定(对齐 Lua 5.1 `read_numeral` 的「贪心吃完 + 整体校验」)**:Lua 5.1 的做法
是**贪心吃掉所有「可能属于数字」的字符**(数字、`.`、`e`/`E`/`+`/`-` 在指数位、hex 位的
`a-f`),然后把整段交给 `str2d`(≈`strtod`)解析;**只要解析没吃完整段或失败就报
`malformed number`**。例如 `1..2`(数字扫描吃 `1.`,剩 `.2`?——实际 `1..` 里第二个 `.` 不被
数字吃,留给 `Concat`)、`0x`(无 hex 位)、`1e`(指数缺数字)、`3.4.5`、`0x1p4`(5.1 视为
`0x1` 后跟标识符 `p4`?——见 §5.3 口径)等都应落 malformed 或拆成相邻 token,**精确边界由
strconv/自写解析的「整段消费」语义决定**(§5.4),并由差分核对(§13)。

> 「near '...'」中的片段:Lua 5.1 报错把当前正在读的 token 文本放进 `near`。本文 `malformed
> number near '<lit>'` 用累积到的 `lit`。**精确措辞(尤其 `near` 的截断规则)待
> [12-testing-difftest](./12-testing-difftest.md) 差分核对**(§11、§13)。

### 5.3 十六进制:仅整数,**不支持** hex 浮点(口径锁定)

Lua 5.1 的 `0x` 字面量**只有整数形式** `0x[0-9a-fA-F]+`,**没有** hex 浮点。形如 `0x1p4`、
`0x1.8p1`、`0xA.8` 的「带二进制指数 `p`/`P` 的十六进制浮点」是 **Lua 5.2 才引入**的特性,
`roadmap.md` (§6) 排除 5.2+。

> **决策:本 lexer 的 hex 数字在吃完 `[0-9a-fA-F]+` 后即终止**,不识别 `.`/`p`/`P`。这与 5.1
> 一致:`0x1p4` 在 5.1 中**被切成 `0x1`(数字)+ `p4`(标识符)两个相邻 token**(因为 `1` 后
> 的 `p` 不是 hex 位、也不触发指数——hex 无 `e` 指数),随后由 parser 当作「数字紧跟标识符」
> 处理(通常是语法错误,但**那是 parser 层的事,不是 lexer 的 malformed**)。`0x1.8` 同理切成
> `0x1`(=1.0)+ `.8`(=0.8)?——实际 `.` 不被 hex 吃,留给后续:`0x1` 然后 `.8` 作数字
> `0.8`,得相邻两数字(parser 报错)。**这些边界是 5.1 的真实行为,本文照搬,并由差分钉死**
> (§13)。
>
> 同样**排除**:八进制无前缀(Lua 无八进制字面量,`010` 就是十进制 `10`)、二进制 `0b`(Lua
> 从无)。

hex 整数转 double:`0xFFFFFFFFFFFFFFFF` 这种超 `int64` 的大 hex,Lua 5.1 也以 double 容纳
(可能丢精度),`parseLuaNumber` 须能把任意位数 hex 整数转 `float64`(§5.4)。

### 5.4 转 float64:走 `value.NumberValue` 保证运行期一致

载荷类型权衡:token 的 `Num` 字段是裸 `float64`(§2),而 [01](./01-value-object-model.md)
§3.4 的 `NumberValue(f float64) Value` 返回的是 **boxed `Value`(`uint64`)**,二者类型不同。
两条等价落地路径:

- **(A) `Num` 存 `float64`,本地复用规范化逻辑**:`parseLuaNumber` 返回已规范化的 `float64`
  (NaN→canonNaN-as-float),codegen 的 `addConst` 再调 `NumberValue` boxing。规范化语义**引自**
  01 §3.4(`if f != f { f = math.Float64frombits(canonNaN) }`),不新增 01 不存在的 API。
- **(B) `Num` 改存 boxed `Value`**:lexer 直接调 `value.NumberValue(f)` 把载荷存成 `Value`。

本文取 **(A)**(token 载荷保持裸 `float64`,与 §2、04 §13「数字已转 float64」措辞一致;boxing
推迟到 codegen)。两者差分无可观察差异,(B) 作备选记 §13。

```go
// 把 Lua 5.1 数字字面量文本转为已规范化的 float64(路径 A)。
// 返回 (规范化值, ok)。ok=false ⇒ malformed。
func parseLuaNumber(lit string) (float64, bool) {
    var f float64
    if len(lit) >= 2 && lit[0]=='0' && (lit[1]|0x20)=='x' {
        u, ok := parseHexInt(lit[2:])   // 任意位数 hex → 累乘 16(超 64-bit 也能落 double)
        if !ok { return 0, false }
        f = float64(u)                  // hex 仅整数,直接转
    } else {
        var err error
        f, err = strconv.ParseFloat(lit, 64)  // 十进制/小数/指数;接受 Lua 5.1 全部十进制形态
        if err != nil && !acceptRange(f, err) { return 0, false }  // ErrRange→±Inf 接受(见下)
    }
    return canonicalizeNaN(f), true     // ★ 规范化(NaN→canonNaN),语义引自 01 §3.4
}

// 与 01 §3.4 NumberValue 内同款规范化(只作用于 float64,不 boxing)
func canonicalizeNaN(f float64) float64 {
    if f != f { return math.Float64frombits(0x7FF8_0000_0000_0000) }  // canonNaN
    return f
}
```

- **十进制/小数/指数**:`strconv.ParseFloat(lit, 64)` 的接受集是 Lua 5.1 十进制数字的超集
  (Go 还接受 `0x1p4` hex 浮点与 `_` 分隔,但**本 lexer 的扫描器根本不会把这些字符喂进
  `lit`**——`lit` 只含扫描器认可的字符,故不会误纳 5.2+ 形态)。**关键自洽**:扫描器负责
  「哪些字符属于一个数字 token」(§5.2/5.3,严格 5.1),`ParseFloat` 只负责「把这段已是 5.1
  合法的文本转 double」。二者职责不重叠,5.2+ 形态被扫描器挡在 `lit` 之外。
- **`canonicalizeNaN` 与 01 同源**:其 NaN→canonNaN 规则**逐字复制** [01](./01-value-object-model.md)
  §3.4 `NumberValue` 内的 `if f != f { f = canonNaN }`,只是不做 boxing(token 载荷是
  `float64`,见上权衡)。**理由**:数字字面量的折叠口径必须与运行期解释器**逐字节同结果**
  (04 §13 第 4 条缺口、04 §5.5 常量折叠):lexer 产的 `float64`(已 canonNaN)、codegen 的
  `addConst`→`NumberValue` boxing、解释器算术,三者共用同一规范化,**NaN/Inf 的 boxing 表示
  天然一致**,差分成立。
  - 注:正常数字字面量**不会**产生 NaN(NaN 无字面量写法);但 `1e400`(上溢 `+Inf`)、
    `-1e400` 等会产 `±Inf`,`ParseFloat` 对极端上溢返回 `±Inf` 且 `err` 为 `ErrRange`——
    **此处口径**:Lua 5.1 把上溢读作 `inf`(不报错)。故 `parseLuaNumber` 对 `ErrRange` 且
    结果为 `±Inf` 应**接受**(返回该 Inf),仅对真正无法解析(语法错)才 `ok=false`。
    精确边界(下溢到 0、`ErrRange` 的处理)**待差分核对**,记入 §13。

> 载荷一致性承诺:`token.Number` 的 `Num` 自此是「值世界合法 double」,parser/codegen 无需
> 再做任何数字规范化,直接进 `NumberExpr.Val`(04 §3.2)→ `addConst`(04 §11)。

### 5.5 数字相关错误

| 触发 | 措辞(暂拟,待差分) |
|---|---|
| `0x` 无 hex 位、`1e` 指数缺位、`3.4.5`、整段无法被解析吃尽 | `malformed number near '<lit>'` |
| 数字紧跟标识符/数字(如 `3a`、`0x1p4` 拆出的 `0x1`+`p4`) | **非 lexer 错误**:lexer 正常切成相邻 token,由 parser 报 `syntax error`(04 §4) |

---

## 6. 字符串字面量

### 6.1 短字符串(单引号 / 双引号)

```
ShortString ::= '"'  ( EscOrChar )* '"'
              | "'"  ( EscOrChar )* "'"
```

- 由 `"` 或 `'` 起,到**同种**引号止;内部可含转义。
- **不可跨「裸换行」**:短字符串内出现未转义的换行(`\n`/`\r`)是**非法**(`unfinished
  string`),除非是 `\<newline>` 续行转义(见下)。到 EOF 仍未闭合 → `unfinished string`。

#### 6.1.1 转义序列(Lua 5.1 全集)

| 转义 | 结果字节 | 说明 |
|---|---|---|
| `\a` | `0x07` | 响铃 |
| `\b` | `0x08` | 退格 |
| `\f` | `0x0C` | 换页 |
| `\n` | `0x0A` | 换行 |
| `\r` | `0x0D` | 回车 |
| `\t` | `0x09` | 制表 |
| `\v` | `0x0B` | 垂直制表 |
| `\\` | `0x5C` | 反斜杠 |
| `\"` | `0x22` | 双引号 |
| `\'` | `0x27` | 单引号 |
| `\<newline>` | `0x0A` | **续行**:`\` 后紧跟换行 ⇒ 写入一个 `\n`,且**行号 +1**(见下) |
| `\ddd` | `byte(ddd)` | **十进制**转义,1~3 位十进制数;值必须 ≤ 255,否则 `escape sequence too large` |

> **明确排除(5.2/5.3,本项目不收)**:
> - `\xHH`(十六进制转义)—— **Lua 5.2** 引入,排除。
> - `\u{XXXX}`(Unicode 码点)—— **Lua 5.3** 引入,排除。
> - `\z`(跳过后续空白)—— **Lua 5.2** 引入,排除。
> - `\ddd` **超过 3 位**或 **> 255** 非法(5.1 限定 1~3 位十进制且 ≤255)。
>
> 遇到未知转义字母(如 `\x`、`\z`、`\q`)→ 报 `invalid escape sequence` /
> `unexpected symbol`(**精确措辞待差分**,§11/§13)。这保证 5.2+ 写法在本项目里**明确报错**
> 而非静默接受。

`\<newline>` 续行细则(对齐 Lua 5.1 `read_string` 中对 `\` 后换行的处理):`\` 后若是
`\n`/`\r`(含 `\r\n`/`\n\r` 组合),则:① 向字符串内容写入一个规范 `\n`(0x0A);② 调用与
§9 同款的 `incLineJoin`,把 `\r\n`/`\n\r`/`\n`/`\r` 当**一个**换行、`line += 1`。即续行既不在
结果里留 `\`,也正确累加行号。

```go
// 短字符串扫描(伪码;buf 复用累积,最后一次性 string(buf))
func (lx *Lexer) scanShortString(quote byte) (token.Token, error) {
    startLine := lx.line
    lx.pos++                       // 跳过开引号
    lx.buf = lx.buf[:0]
    for {
        if lx.pos >= len(lx.src) { return zero, lx.errAt(startLine, "unfinished string") }
        c := lx.src[lx.pos]
        switch {
        case c == quote:
            lx.pos++
            return token.Token{Kind: token.String, Line: startLine, Str: string(lx.buf)}, nil
        case c == '\n' || c == '\r':                 // 裸换行非法
            return zero, lx.errAt(startLine, "unfinished string")
        case c == '\\':
            lx.pos++
            if err := lx.readEscape(); err != nil { return zero, err }   // 写入 buf,处理续行/\ddd
        default:
            lx.buf = append(lx.buf, c); lx.pos++
        }
    }
}
```

注意 token 的 `Line` 取 `startLine`(起始行),即便字符串因 `\<newline>` 续行跨了多行。

### 6.2 长字符串(长括号 `[[ ]]` / `[=[ ]=]`)

```
LongString ::= '[' '='* '[' ... ']' '='* ']'        -- level = 等号个数,开闭必须同 level
```

- **开长括号**:`[` 后跟 `n` 个 `=`(`n≥0`),再跟 `[`。`n` 称 **level**。`[[` 是 level 0,
  `[=[` 是 level 1,`[==[` 是 level 2,以此类推。
- **闭长括号**:`]` + 恰好 `n` 个 `=` + `]`(同 level)。level 不匹配的 `]=]` **不**结束串,
  当普通内容。
- **首换行丢弃**:**紧跟开长括号的第一个换行被丢弃**(对齐 Lua 5.1:`[[` 后若立即是换行,该
  换行不计入内容)。如 `[[\nabc]]` 的内容是 `"abc"` 而非 `"\nabc"`。该丢弃的换行仍要 `line
  += 1`(§9)。
- **内部不处理转义**:长字符串里 `\n`、`\t`、`\\` 等**字面照搬**,无转义。
- **可含任意字节**(包括其它 level 的长括号片段、引号),直到匹配的同 level 闭括号。

```lua
a = [[hello\nworld]]      -- 内容 = 反斜杠 n,literally:  hello\nworld(10+? 字节,\ 与 n 各一)
b = [==[ has ]] and ]=] inside ]==]   -- level 2;内部的 ]]、]=] 都是普通内容
```

长字符串的扫描复用 §8 的**长括号公用子程序**。token 的 `Line` 取开括号所在行;内部每遇换行
`line += 1`(§9)。

### 6.3 行号维护(跨行字符串)

- 短字符串:仅 `\<newline>` 续行会跨行,每次续行 `line += 1`(§6.1.1);token `Line` 仍是
  起始行。
- 长字符串:内容可含任意多换行,扫描时每个换行(§9 四形态)`line += 1`;token `Line` 仍是
  **开括号**行。

这样 04 拿到的每个 `String` token 的 `Line` 都指向其**字面量起点**,与「带行号的 token」契约
一致,后续 `ast.*Expr{Line}`(04 §3)即用此值。

---

## 7. 注释

注释**被 lexer 直接吞掉,不产 token**(parser 永远看不到注释)。两种:

### 7.1 短注释

```
-- ...直到行尾(不含换行;换行本身按 §9 处理)
```

`--` 起,到**行尾**(下一个 `\n`/`\r` 之前)全部丢弃。换行不被注释吃掉(留给 §9 计行)。到
EOF 也正常结束(末行可无换行)。

### 7.2 长注释

```
--[[ ... ]]      --[=[ ... ]=]   ...  （--  紧跟长括号)
```

`--` 之后**紧跟开长括号**(`[` + `=`* + `[`)即长注释,复用 §8 长括号扫描,扫到匹配同 level
闭括号为止,整体丢弃。长注释**可跨行**,内部换行照常 `line += 1`(§9)。

**消歧:`--` 后是否长注释**——`--` 之后必须能成功解析出**开长括号**(`检测 level ≥ 0 且其后
紧跟第二个 `[``)才是长注释;否则退化为短注释。判定用 §8 的「试读 level」helper(只前瞻、
不消费失败的部分):

```
扫到 "--":
  若其后是 '[' 且 试读长括号 level 成功 → 长注释,longBracketScan(level)
  否则 → 短注释,吃到行尾
```

> 边角:`--[=` 后**不是** `[`(如 `--[==x`)→ 不是合法开长括号 → 退化为短注释,从 `--` 吃到
> 行尾(`[==x` 都被当短注释内容丢弃)。这与 Lua 5.1 一致(`--[` 不构成长括号时整行注释)。
> 长注释未闭合到 EOF → `unfinished long comment`(§11)。

---

## 8. 长括号公用子程序(长字符串 + 长注释共用)

长字符串(§6.2)与长注释(§7.2)共享「**读 level + 扫到匹配闭括号**」逻辑,抽成 helper,避免
两处重复、保证语义一致(这也是 Lua 官方 `read_long_string` 同时服务字符串与注释的做法)。

```go
// 试读开长括号的 level。
//   成功:返回 (level, true),并消费 "[" + level 个 "=" + "[";
//   失败:返回 (_, false),【不消费】任何字节(供 §7.2 注释消歧回退)。
func (lx *Lexer) tryOpenLongBracket() (level int, ok bool)

// 已消费开长括号后,扫长括号体到匹配的同 level 闭括号。
//   isComment=false:把内容写入 lx.buf(供长字符串)。
//   isComment=true :丢弃内容(供长注释)。
//   处理:首换行丢弃(仅长字符串语义需要 buf;注释也吃掉但无所谓)、内部换行计数、未闭合报错。
func (lx *Lexer) scanLongBracketBody(level int, isComment bool) error
```

`tryOpenLongBracket` 逻辑:

```
设 p = 当前位置,要求 src[p]=='['
  q := p+1
  count := 0
  while src[q]=='=' { count++; q++ }
  if src[q]=='[' {                 // 合法开长括号
      lx.pos = q+1                 // 消费 [ ='s [
      return count, true
  }
  return 0, false                  // 不消费(pos 不动)
```

`scanLongBracketBody` 逻辑:

```
// 1) 首换行丢弃
if 当前是换行(§9 四形态) { incLine();  /* 不写入 buf */ }
// 2) 主循环
for {
    if EOF { return errf(isComment ? "unfinished long comment" : "unfinished long string") }
    if 当前是 ']' 且 紧跟 level 个 '=' 且 再紧跟 ']' {
        消费闭括号; return nil
    }
    if 当前是换行 { incLine(); if !isComment { buf.append('\n') }; continue }  // 换行规范为 \n
    if !isComment { buf.append(当前字节) }
    pos++
}
```

要点:
- **闭括号匹配**:必须恰好 `]` + `level` 个 `=` + `]`。`level` 不符的 `]`(如 level 2 时遇
  `]]` 或 `]=]`)是普通内容,继续。
- **换行规范化**:长字符串内容里的换行统一写为单个 `\n`(0x0A),与 Lua 5.1 行为一致(无论源
  是 `\r\n` 还是 `\r`,存入串的都是 `\n`);同时 `line += 1`。
- **首换行丢弃只发生一次**(开括号紧后),不影响内容中的其它换行。

> **可选告警:嵌套长括号(`nesting of [[...]] is deprecated`)**。Lua 5.1 对「长字符串/长
> 注释内部又出现 `[[`(level 0)」会发**告警/错误**(5.1 把 level-0 长括号内嵌的 `[[` 视为不
> 推荐/报错,5.2 起改为允许任意嵌套)。本项目**P1 暂不实现该告警**(纯设计阶段无诊断渠道),
> 行为上把内部 `[[` 当普通内容。**措辞 `nesting of [[...]] is deprecated` 及是否要复刻该告警
> 待差分核对**(§11/§13)。

---

## 9. 空白与换行

### 9.1 空白

跳过(不产 token):空格 `0x20`、制表 `\t`(0x09)、垂直制表 `\v`(0x0B)、换页 `\f`
(0x0C)、回车 `\r`、换行 `\n`。即 Lua 5.1 `isspace` 集。其中 `\r`/`\n` 还要触发计行(§9.2)。

### 9.2 换行规范化:四形态计 1 行(`incLine` 语义)

对齐 Lua 5.1 `inclinenumber`:把 **`\r\n`、`\n\r`、`\n`、`\r`** 四种序列都当作**一个**换行,
`line += 1`。

```go
// 在已确认当前字节是 '\n' 或 '\r' 时调用。消费一个换行序列,line++。
func (lx *Lexer) incLine() {
    old := lx.src[lx.pos]          // '\n' 或 '\r'
    lx.pos++
    if lx.pos < len(lx.src) {
        c := lx.src[lx.pos]
        if (c == '\n' || c == '\r') && c != old {  // 配对的另一半:\r\n 或 \n\r
            lx.pos++
        }
    }
    lx.line++
    // 行号溢出保护:Lua 5.1 在行号超上限时报 "chunk has too many lines";
    // 本项目 line 是 int32,实际不会溢出,记 §13。
}
```

- 关键:`c != old` 保证 `\n\n`(两个独立换行)不被吞成一个——只有「`\r` 后 `\n`」或「`\n` 后
  `\r`」这种**异种**配对才算同一换行。`\n\n` 是两行,`\r\r` 是两行。
- 行号**从 1 起**(`Lexer.line` 初值 1)。token 的 `Line` 即取当时 `lx.line`。
- 续行转义 `\<newline>`(§6.1.1)、长括号首换行(§8)、长字符串内换行(§6.2)都调 `incLine`,
  行号口径**全程统一**。

---

## 10. 首行特殊处理(shebang `#!`)

Lua 5.1 的 `luaL_loadfile`:**若 chunk 首字符是 `#`,跳过首行**(到第一个换行),以支持
`#!/usr/bin/lua` 这类可执行脚本头。注意这是 `#`(不限 `#!`)在**首字节**的特例——Lua 5.1
为此特地在加载器层把首行替换/跳过。

> **决策:shebang 跳过归 loader / 嵌入层职责,不在 lexer 核心。** 理由:
> 1. **`#` 在 Lua 是合法算符**(一元长度 `#t`,§3.1 `Hash`)。若 lexer 无条件「首字符 `#` 跳
>    行」,会与「源码确实以 `#expr` 起头」的合法(虽罕见)程序冲突。Lua 5.1 把这个 hack 放在
>    **文件加载器**(`luaL_loadfile`),而非词法核心(`llex`),正是因为它是「文件可执行约定」
>    而非语言词法。
> 2. 望舒的入口是 `Compile`(`roadmap.md` (§8)、[11-embedding-arena-abi](./11-embedding-arena-abi.md)),
>    源可能来自字符串/文件/嵌入。**shebang 只对「文件源」有意义**。
>
> **落地**:在**加载源码的边界**(未来 `cmd/wangshu` 脚本运行器读文件时,或 `Compile` 的文件
> 变体)做预处理:`if len(src)>0 && src[0]=='#' { 跳到第一个 \n(含),其余喂给 lexer }`。
> **被跳过的首行仍占 1 行**:loader 跳行时应让 lexer 的初始 `line` 从 2 起(或在源里保留换行
> 让 lexer 自然计行),使错误行号与原文件对齐。**`lex.New` 接受一个可选初始行号**或由 loader
> 保留首行换行——二者皆可,**精确接口待 [11](./11-embedding-arena-abi.md) 定稿**,记 §13。

**lexer 本体不识别 `#!`**:`#` 在 lexer 里恒为 `Hash` token。这样 lexer 保持「纯词法、与源
来源无关」,可被 REPL / 字符串 eval / 文件加载共用。

---

## 11. 错误报告

### 11.1 定位与措辞格式

lexer 错误携带:**chunk 名(`source`)+ 行号(出错处或 token 起始行)+ 措辞**。格式对齐 Lua
5.1 的 `luaX_syntaxerror`/`luaX_lexerror`:

```
<source>:<line>: <message> near '<token-text>'
```

如 `@a.lua:3: unfinished string near '"abc'`。`near '<...>'` 段在 Lua 5.1 里放「当前 token
的文本」;对未闭合类错误放已读到的片段。

```go
func (lx *Lexer) errf(format string, a ...any) error            // 当前行
func (lx *Lexer) errAt(line int32, format string, a ...any) error // 指定行(如串/注释起始行)
// 返回的 error 实现 LexError{Source string; Line int32; Msg string},parser 透传为语法错误
```

错误类型 `LexError` 让 parser(04)能区分「词法错」与「语法错」,但二者最终都进 04 §13 的
「首错即停」路径(P1 不做错误恢复)。

### 11.2 错误目录表

| 触发条件 | 措辞(暂拟) | 备注 |
|---|---|---|
| 短字符串到 EOF / 裸换行未闭合 | `unfinished string` | 行号=串起始行 |
| 长字符串到 EOF 未闭合 | `unfinished long string` | 措辞**待差分核对** |
| 长注释到 EOF 未闭合 | `unfinished long comment` | |
| `\ddd` > 255 或位数非法 | `escape sequence too large` | 5.1 仅 1~3 位十进制且 ≤255 |
| 未知转义字母(含 5.2+ 的 `\x`/`\z`、5.3 的 `\u`) | `invalid escape sequence` | **措辞待差分**;关键是 5.2+ 转义在此**明确报错** |
| 数字解析失败 / 整段未被吃尽 | `malformed number near '<lit>'` | §5;`near` 截断规则待差分 |
| 非法/不可识别字符(高位字节、控制字符、孤立 `~` 后非 `=` 等) | `unexpected symbol near '<char>'` | 含 ASCII-only 标识符策略(§4.3)挡下的高位字节 |
| 长括号定界符畸形(`[=` 后非 `[`,在期望长串处) | `invalid long string delimiter` | **措辞待差分**;多见于 parser 期望 `[` 却得畸形长括号 |
| (可选)长串/注释内嵌 `[[` | `nesting of [[...]] is deprecated` | **5.1 行为存疑,P1 暂不实现**,§8/§13 |

> **凡标「待差分核对」的措辞,均以 [12-testing-difftest](./12-testing-difftest.md) 对拍 Lua
> 5.1 参考实现/gopher-lua 的实际输出为准**,本文不编造确定性文案(避免与差分基准不符)。错误
> **种类与触发条件**是确定的;**精确字符串**待钉。

---

## 12. 对 04 的契约兑现(回应 04 §13 第一条缺口)

04 §13 第一条缺口原文要点:「lexer 产出**带行号的 token、数字已转 float64、长字符串/转义已解
码、长注释已剥离**」,并问「token 字段、种类枚举、字面量载荷、**LL(2) 前瞻是 lexer peek 还是
parser 自缓存**」。逐条兑现:

| 04 的依赖点 | 本文的定死答案 | 出处 |
|---|---|---|
| **带行号的 token** | `Token.Line int32`,从 1 起;多行字面量取**起始行** | §2、§6.3、§9 |
| **数字已转 float64** | `Token.Num float64`,经 `value.NumberValue` 规范化;parser 直接进 `NumberExpr.Val` | §2、§5.4 |
| **字符串已解码** | `Token.Str string`,短串转义已展开、长串已剥壳并丢首换行;**未 intern**(intern 在 codegen,对齐 01 §5.1 / 04 §11) | §2、§6 |
| **长注释已剥离** | 注释(短/长)在 lexer 直接吞掉,**不产 token**;parser 永不见注释 | §7 |
| **token 字段** | `{Kind, Line, Num, Str, Name}`(§2 struct) | §2 |
| **种类枚举** | `token.Kind`:EOF + 3 字面量 + 21 关键字 + 25 符号/算符;**不含 5.2+** | §3 |
| **字面量载荷形态** | 数字=`float64`、字符串=Go `string`、标识符=`string`;至多一个有效 | §2 |
| **LL(2) 前瞻归属** | **parser 自缓存一格**(`ahead`/`hasAhead`),lexer 只提供 `Next()`;对齐官方 `t`/`lookahead` 双 token | §1.2 |
| **04 §4.1 `Parser{tok,ahead,hasAhead}` 调用方** | 由 `Lexer.Next()` 供给;`p.next()`/`p.peek()` 驱动模型见 §1.2 | §1.2 |
| **04 §3.4 运算符枚举映射** | 本文符号 token(`Plus..Ellipsis`,§3)是 04 `BinOp/UnOp` 的词法来源;parser 把 token→AST 运算符 | §3.1、04 §4.3 |

> 一句话:**04 可以把本文 §2 的 `Token` 与 §3 的 `Kind` 当作既定接口直接编码**;04 §13 第一条
> 缺口在本文定稿后**可关闭**(回填动作记 §13)。

---

## 13. 不变式清单 + 文档缺口 / 待决

### 13.1 不变式清单(实现与差分须守)

1. **行号从 1 起、四换行形态计 1 行**:`\r\n`/`\n\r`/`\n`/`\r` 各算一行,`incLine` 唯一入口
   (§9);所有跨行场景(续行、长括号、长串)共用它。
2. **token `Line` = 字面量/词素起始行**:多行 token 不取结束行(§2、§6.3)。
3. **数字载荷已规范化**:`Token.Num` 必经 `value.NumberValue`,与运行期 NaN/Inf 表示一致
   (§5.4、[01](./01-value-object-model.md) §3.4);折叠口径自洽(04 §13 第 4 条)。
4. **字符串载荷是已解码 Go `string` 且未 intern**:intern 是 codegen 职责(§2、§6、04 §11、
   [01](./01-value-object-model.md) §5.1)。
5. **注释零 token**:短/长注释完全吞掉(§7)。
6. **5.1 封闭集**:`Kind` 不含任何 5.2+ 终结符;5.2+ 转义/数字写法**明确报错或按 5.1 切分**,
   绝不静默接受(§3、§5.3、§6.1.1)。
7. **长括号单一实现**:长串与长注释共用 §8 helper,level 匹配语义一处定义。
8. **lexer 纯词法、与源来源无关**:shebang 不在 lexer(§10);`Next()` pull 式、无内部 peek
   (§1.2)。

### 13.2 文档缺口 / 待决(记入 `llmdoc/memory/doc-gaps.md`)

- **错误措辞待差分核对**:`unfinished long string`、`invalid long string delimiter`、
  `invalid escape sequence`、`malformed number near '...'` 的 `near` 截断、
  `nesting of [[...]] is deprecated` 是否复刻——全部以
  [12-testing-difftest](./12-testing-difftest.md) 对拍 Lua 5.1 实际输出为准(§11)。**错误
  种类已定,精确字符串待钉。**
- **非 ASCII 标识符**:本文定死 ASCII-only(§4.3)。若差分基准在高位字节上与之不一致,需在
  [12](./12-testing-difftest.md) 固定口径(可能需放宽到「locale-free 的某确定集」)。
- **shebang 归属的精确接口**:`#` 首行跳过归 loader/嵌入层(§10),但 `lex.New` 是否接受
  「初始行号」参数、还是由 loader 保留首行换行让 lexer 自然计行,**待
  [11-embedding-arena-abi](./11-embedding-arena-abi.md) 定稿**。
- **`lex` 是否依赖 `value`**:本文选「lexer 处即 `NumberValue`」(§1.1、§5.4)。若实现期
  `lex→value` 依赖不便,允许退化为「lexer 产裸 `float64`、codegen 处规范化」,**载荷类型不变**,
  差分无可观察差异——二选一待实现期定。
- **数字上溢/下溢口径**:`1e400`→`+Inf`、极小数下溢→`0`、`ErrRange` 的接受/拒绝边界须与
  Lua 5.1 `str2d` 逐字节对齐(§5.4),待差分。
- **回填 04**:**已执行**——04 §13 第一条缺口已标注关闭(token 字段/枚举/载荷/LL(2) 前瞻归属均按本文 §2/§3 固化)。
- **token 是否需列号/起止偏移**:P1 仅带行号(§2)。未来 source map / 精细诊断可能需要列号,
  当前未加。
- **行号溢出**:Lua 5.1 有 `chunk has too many lines`;本项目 `line int32` 实际不会溢出,
  暂不实现该检查(§9)。

---

相关:[01-value-object-model](./01-value-object-model.md) ·
[02-bytecode-isa](./02-bytecode-isa.md) ·
[04-frontend-parser-codegen](./04-frontend-parser-codegen.md) ·
[12-testing-difftest](./12-testing-difftest.md) ·
[../architecture](../architecture.md) ·
[../../../llmdoc/architecture/value-representation](../../../llmdoc/architecture/value-representation.md)
