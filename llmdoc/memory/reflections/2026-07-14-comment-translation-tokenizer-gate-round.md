# 全仓中文注释译英轮(2026-07-14,PR #141,分支 `chore/translate-comments-to-english`)

> 单会话把仓库全部源代码(305 个 `.go` + 6 个 `.s`,311 文件)里的中文代码注释译成英文,严格只动注释、字符串字面量与代码零改动;CI 全绿(conformance / difftest / fuzz-smoke / oracle-smoke / golden-diff-guard / lint 全平台全 variant),review bot APPROVE 且明确「无阻塞、无重要、无小问题」。

## 背景

CLAUDE.md 语言约定:沟通/文档用中文,**代码注释和 commit message 用英文**(2026-06-29 起,见用户 memory `feedback_code_language_english.md`)。仓库历史遗留大量中文注释(动手前全树约 14647 行注释含中文),本轮做一次性清理。**范围边界是本轮的核心争议点**:CLAUDE.md「代码注释」明确列举 godoc / inline / block / `.s` 汇编注释,**不含**字符串字面量(测试 fixture 里的 Lua 源码、与 oracle 逐字节比对的错误消息、`t.Errorf`/`t.Skip`/`t.Logf` 诊断消息),也不含 Makefile / shell 脚本的 `##` 帮助文本。

## 教训

### ① 大规模机械翻译派并行子代理,必须配一个「只动注释」的 tokenizer 权威闸门,不能靠 grep 抽查

本轮派了 ~30 个并行 general-purpose 子代理分批翻译。**子代理在两个维度不可靠**:(a) 相当一部分子代理返回「Hi! How can I help you today?」根本没干活(flaky,需重派);(b) **干活的子代理会越界翻译字符串字面量**——19 个文件里 289 处 `t.Skip("...升层不被支持")`/`t.Errorf("...")`/Lua 源码 fixture 被译成英文。字符串字面量属于运行时行为的一部分(尤其 difftest / oracle 逐字节比对的错误消息、`probes_test.go` 里的 Lua 源码),改了就是改行为。

`grep -P '//.*[CJK]'` 类抽查**发现不了字符串字面量越界**(它只看注释),也会**假阳**(把 `·`/`÷`/中文标点误判)。真正可靠的验证是**用 `go/scanner` 逐 token 比对工作树与 master**:对每个 `.go` 文件,只有 `COMMENT` token 允许变化,`STRING`/`CHAR`/`IDENT`/所有代码 token 流必须与 master 完全一致。这个闸门同时给出两个保证——「只动了注释」+「字符串字面量 == master」——一次性证明零代码改动。19 处越界就是靠它定位并逐字节还原的(还原后闸门复跑 0 mismatch)。

**纪律**:任何「批量只改注释 / 只改格式 / 只改某一类 token」的大规模机械改动,验证不能用 grep,要用 tokenizer 逐 token diff against baseline。grep 是发现问题的启发式,tokenizer 是证明「只动了该动的」的权威。这是 [[prove-the-path-under-test]]「绿色 ≠ 在测你以为在测的」在**改动范围**维度上的对偶:CI 全绿只证明行为没坏,不证明「只动了注释」——difftest 恰好没覆盖到被越界翻译的那条错误消息时,绿色会漏过越界。

### ② grep 的字符集范围会漏中文标点,byte-mode perl 会把多字节符号误判——最终判据要用 UTF-8 感知的 Unicode property

清理接近尾声时 `[\x{4e00}-\x{9fff}]`(CJK 表意文字区)报 0,但仍有 13 处遗留:全角标点 `。：，；（）`(U+3002 等,在 CJK Symbols and Punctuation 区 U+3000–U+303F,不在表意文字区)混在英文注释里(翻译时忘了把句尾 `。` 改成 `.`)。反过来,byte-mode perl 会把 `·`(U+00B7,Plan9 asm 的 `·dispatchInlineHelper`)和 `÷`(U+00F7,benchmark 注释里的除号)的字节误判成 Han。

**纪律**:注释语言的最终判据用 Python `unicodedata` 或 `grep -P` 配 `\p{Han}` + 显式列全角标点区间(`\x{3000}-\x{303f}` `\x{ff00}-\x{ffef}`),并确保按 UTF-8 解码而非按字节。同时保留一条 U+FFFD mojibake 扫描(`\x{fffd}`)——批量重写极易引入乱码。

### ③ 范围边界(哪些算「代码注释」)先对齐,别默认扩大到 Makefile/脚本

CLAUDE.md 对「代码注释」的列举是 Go + `.s`。Makefile 的 `## 全仓静态检查` 帮助文本、shell 脚本注释虽也是中文,但不在这个明确列举里。本轮**主动收窄**到 `.go`/`.s`,把 Makefile/脚本留作可选 followup 并在收尾明确告知用户,而非自作主张一起改(那会让 diff 面失控、也偏离约定字面)。字符串字面量里的中文(fixture Lua 源码、诊断消息)同理——保留,因为它们是行为/数据不是注释。

## 工作流确认

- **flaky 子代理的兜底 = 主助理亲自扫尾**:重派几轮后仍有 churn 时,直接读剩余文件自己译(尤其收敛到最后几个文件时),比无限等待子代理通知快且可靠。与 [[multi-doc-drafting]]「同篇连续失败主助理亲写」同源。
- **commit-msg hook 拒非 ASCII**:em-dash `—` 在 commit message 里被拒,用 ASCII hyphen;body 里的 `→` 也要避开。
- **pre-push self-wrapper** 正常:外层 push「remote rejected」是预期,真实 verdict 看 CI;新分支首推 PR 未建时 hook 跳过 CI watch,需 `gh pr create` 后手动 `make check-pr-ci`(见用户 memory)。

## promotion 决策

教训 ①(大规模机械改动用 tokenizer 逐 token diff 证明范围,而非 grep 抽查)**首次样本,暂留观察**——这是 [[prove-the-path-under-test]] 家族「改动范围」新维度(以往实例都在「路径被执行」/「度量单位」维度),若后续再遇批量机械改动(格式化 / 重命名 / 另一轮注释处理)复现,可升 guide 或并入 [[prove-the-path-under-test]] 作「改动范围断言」节。教训 ②③ 属本任务具体手法,记此备查不升 guide。

## 触发场景

- 做「批量只改某一类 token」的大规模机械改动时(只改注释 / 只改格式 / 批量重命名):动手前先想好 tokenizer 逐 token diff against baseline 的验证脚本,别靠 grep 抽查证明「只动了该动的」。
- 派并行子代理做机械翻译/改写时:预期部分子代理 flaky(空转)+ 部分越界(改了不该改的),收尾必须有一个不依赖子代理自觉的权威闸门。
- 判定注释里是否还有中文时:用 UTF-8 感知的 `\p{Han}` + 显式全角标点区间,别只查 CJK 表意文字区;同时扫 U+FFFD。
- 遇到「哪些算代码注释」的范围问题时:对齐 CLAUDE.md 字面列举(Go + `.s`),字符串字面量 / Makefile / 脚本默认不在内,收窄并告知用户而非自行扩大。
