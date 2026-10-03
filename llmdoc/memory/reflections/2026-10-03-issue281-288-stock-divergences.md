---
name: 2026-10-03-issue281-288-stock-divergences
description: >
  #276-#279 本地审查登记的八处存量差异(#281-#288)在分支 `fix/281-288-stock-divergences` 一起修。教训:
  **降低 GC 扫描范围这类「只是少扫一点」的改动，会把潜伏的悬空引用变成可见的错值**(#285 降 top 后暴露出 pcall 错误展开
  和协程错误死亡都没有关闭 upvalue);**按 issue 原文修之前先拿源码的报错点逐个列清单**(#282 原文 4 条，按
  `lparser.c` / `llex.c` 列出来是十几类，还有长串换行这样的值差异);**依赖 C 调用深度的期望值不能用独立 lua5.1 生成**
  (它比嵌入式 PUC 多一层 `lua_cpcall`,要用 `internal/oracle`);**先建完 AST 再生成代码的结构对不上 codegen 阶段的
  `near` 与行号**,用户决定作为已知限制。
metadata:
  type: reflection
  date: 2026-10-03
---

# #276-#279 本地审查登记的八处存量差异(2026-10-03,issue #281-#288)

> 范围：分支 `fix/281-288-stock-divergences`。改动在 `internal/crescent/{coroutine,host,meta,state}.go`、
> `internal/stdlib/{stdlib,tablelib,baseenv}.go`、`internal/bytecode/chunkid.go`、`internal/frontend/{token,lex,parse,compile}`;
> 测试在 `test/regression/issue28{1,2,3,5,6,7,8}_*_test.go`(#284 的用例加在已有的 `syntax_error_chunkname_test.go`),共用 `stock_divergence_helper_test.go` 里的 `printedBy` / `loadMessage`;
> 设计稿 03 / 04 / 05 / 06 / 07 / 08 / 09 / 10 与 P1 implementation-progress。对账表在 implementation-progress 的
> #281-#288 条目，这里只记过程和教训。

## 1. 降低 GC 扫描范围，暴露出没关的 upvalue(#285)

#285 的修法本身很小:`callHost` 在调用宿主函数前把 top 降到最后一个参数之后，和 `luaD_precall` 对 C 函数的处理一样，
GC 就不再把调用方寄存器里、位于调用之上的旧值当根。

第一次跑全套件,luasuite `closure.lua:95` 失败。原因不在新改动：pcall 的错误展开(`callLuaFromHostNamed`)和协程因错误
死亡(`Resume`)都没有关闭开放 upvalue,出错前创建的闭包仍指着栈槽。以前 top 一直停在帧的寄存器上限，那些栈槽总在扫描
范围里、也很少被复用，问题看不出来;top 降下来以后,GC 会把 top 之上的槽清成 nil,闭包读到的就是 nil。

**教训**:「少扫一点」「早回收一点」这类改动会把潜伏的悬空引用从「多活一会儿」变成「读到错值」。和
[[2026-06-12-longevity-review-fix-round]] 里「内存复用会把良性 bug 变成致命 bug」是同一类：动扫描范围前，先查哪些
引用原先靠「范围够大」才没出事。这次是 upvalue 的关闭点，对照 `luaD_pcall` 的 `luaF_close(L, oldtop)` 补上。

## 2. issue 原文的范围远小于实际差异(#282)

#282 原文列了 4 种写法和 1 条评论补充。按 `lparser.c` 的 `error_expected` / `check_match` / `errorlimit` /
`luaX_syntaxerror` 和 `llex.c` 的 `luaX_lexerror` 逐个列报错点，再用探针和 lua5.1 比对，差异有十几类：`exprstat` 的判断
依据、名字检查的引号、`check_match` 的 `(to close ...)`、错误行号取扫描器所在行、上限检查的时机和措辞、词法错误的
`near` 内容、不认识的字符当作 token、linedefined 取 `(` 所在行。其中长串里 `\r\n` 存成两个字节是**值的差异**,不只是
措辞;块名里的 NUL 也是这样顺带查出来的。

**教训**:措辞类 issue 不要按原文的几条修，先从参考实现的源码把同一机制的所有报错点列出来，逐个写探针。用户在看到
清单后决定了「全部对齐」;如果只修原文那几条，剩下的会在下一轮审查里一条条冒出来。

## 3. 依赖 C 调用深度的期望值要用内嵌 oracle 生成

赋值目标的上限是 `LUAI_MAXCCALLS - nCcalls`,和当时的 C 调用深度有关。用独立的 `lua5.1` 生成期望值，结果比望舒少 1。
这不是望舒的错：独立解释器在 `lua_cpcall(pmain)` 里多套了一层，上一轮已经把这一层登记为已知限制，嵌入式 PUC 与
望舒一致。这三条期望值改用 `internal/oracle` 内嵌的 5.1.5 生成，并在测试头注里写明原因。

**教训**:凡是结果依赖 C 调用深度的写法(语法层数、赋值目标数、C stack overflow 的层数),期望值从内嵌 oracle 取;
用独立解释器生成会把「宿主多一层」的差异当成被测代码的错。

## 4. 结构差异导致对不上的部分，交给用户决定

lua5.1 一边扫描一边生成代码,codegen 阶段的错误(`function or expression too complex`、`control structure too long`)
带 `near '<当前记号>'`,行号是扫描器当时所在的行。望舒先建完整个 AST 再生成代码，生成时已经不知道当时的记号。要对齐，
得给 AST 里所有记录行号的位置加上记号信息，改动 codegen 里约 30 个带行号参数的函数，而这些正是 #248 / #252 / #262
刚对齐过的行号逻辑。另有 `luaM_growvector` 上限按运行期错误抛出的两处。我把代价和触发条件(只有超大函数会遇到)
说清楚后交给用户，用户决定作为已知限制，记在 04 §9,同时把这一轮已经对齐的寄存器上限(249)单独提交。

## 5. 流程上的小事

- pre-commit 的 golangci-lint 拒了一次(staticcheck 的 De Morgan 提示),提交没有生成。提交前就有的检查结果作废，改完后
  重新暂存、重新记录，不要把作废的那次当成这次提交的依据。本地跑 `go vet` 不等于跑过 lint,改了 Go 代码先跑一次
  `golangci-lint run` 再提交。
- 用 Python 整段替换源码时，替换串里的缩进和源文件对不上，断言失败、文件没改。之后改用 Edit 逐处改，或在替换前打印
  匹配次数。
