# 参考:测试目录布局

> 测试代码住在哪里、新测试往哪放、fuzz 语料和取证设施在哪。状态:2026-09-09 根目录测试整体迁入 `test/` 后的布局;此前根目录有 47 个 `_test.go`。

## 一句话规则

**仓库根目录不放 `_test.go`。** 根 package(`wangshu`)只有实现文件;它的行为由 `test/` 下的外部测试包
(`package xxx_test`,只经公开 API)覆盖。`go test .` 报 `[no test files]` 是预期状态,不是遗漏。

## 目录一览

| 目录 | package | 放什么 | 典型 build tag |
|---|---|---|---|
| `test/api/` | `api_test` | 宿主嵌入 API 面:State / Program 生命周期、arena 选项与 ABI、context 取消、io 句柄、沙箱与硬化、globals 槽、类型化数组表、预分配表 | 默认;`embedding_admin_default`(`!wangshu_p3`)/ `embedding_admin_p3`(p3+profile) |
| `test/language/` | `language_test` | 语言与标准库语义:stdlib、table / table_puc51、meta、coroutine、global、register、getinfo、os.time isdst、baseline,以及 concurrency / longevity 压力测试 | 默认 |
| `test/tiering/` | `tiering_test` | 分层与晋升:tier 开关、promotion 计数、P2 bridge 端到端与阈值校准 | `wangshu_profile` 系列(p1 / p3 / p4 各有专属文件) |
| `test/regression/` | `regression` | issue 回归:某个 crasher / 分歧被修好后钉住的显式测试。命名 `issueNNN_*_test.go`;2026-09-09 从根目录并入的 5 个仍叫 `fuzz_NNN_test.go`,内容同类 | 按 issue 所在层各异 |
| `test/fuzz/` | `fuzz_test` | 五个 `Fuzz*` harness + `raceEnabled` 常量 + `main_test.go`(`TestMain`)+ **`testdata/fuzz/` 语料** | 每个 harness 一族,见下 |
| `test/testutil/` | `testutil` | 跨包共用的测试辅助函数。**只有第二个包需要时才进这里**,单消费者的辅助函数留在自己包内 | 无 |
| `test/conformance/` `test/difftest/` `test/luasuite/` | 各自 | 迁移前就在这里,未动 | 各自 |
| `internal/fuzzforensics/` | `fuzzforensics` | fuzz worker 静默死亡的取证:fd 2 尸检 + 飞行记录仪。**非测试代码**,用内部测试包做单元测试 | 无 |
| `internal/fuzzbudget/` | `fuzzbudget` | P4 fuzz harness 的 step budget 常量,harness 与回归测试共用 | 无 |

## test/fuzz 的 build tag 家族

一个 package,文件按 tag 分家;默认 build 下只剩 `fuzz_test.go`(`FuzzCompileRun`)和 `main_test.go`。

```text
fuzz_test.go                默认
fuzz_auto_test.go           (wangshu_p3 || wangshu_p4) && wangshu_profile
fuzz_p4_test.go             wangshu_p4 && wangshu_profile
fuzz_oracle_test.go         wangshu_oracle_cgo && cgo
fuzz_oracle_tiered_test.go  wangshu_oracle_cgo && cgo && (wangshu_p3 || wangshu_p4)
race_on/off_test.go         race / !race,只定义 raceEnabled,供 fuzz_auto / fuzz_p4 跳过 -race 下的 mmap 路径
```

`test/regression/` 也有自己的一份 `race_on/off_test.go`,同样只定义 `raceEnabled`。两份互不可见,按包各定义一份
是既有做法。

## 语料与重放

- `test/fuzz` 五个靶点的语料在 **`test/fuzz/testdata/fuzz/<FuzzTarget>/<hash>`**;其他包的靶点语料在各自包目录下(见下文)。
  Go 的 corpus 查找是相对 harness 所在包目录的,语料跟 harness 必须同包;根目录没有兼容位置。
- 从仓库根重放一个 seed(包路径换成靶点所在的包):
  ```bash
  go test ./test/fuzz -run '^FuzzCompileRun/<hash>$' -count=1 -v
  CGO_ENABLED=1 go test -tags 'wangshu_oracle_cgo wangshu_p4 wangshu_profile' ./test/fuzz \
      -run '^FuzzOracleDiffTiered/<hash>$' -count=1 -v
  go test ./internal/stdlib -run '^FuzzPattern/<hash>$' -count=1 -v
  ```
  nightly issue 模板生成的重放命令就是这个形式,包路径取自 `scripts/go-fuzz.sh` 打印的
  `fuzz: <pkg> :: <func>` 横幅;2026-09-09 之前的 issue 里写的 `go test .` 已失效,把 `.` 换成靶点所在的包即可。
- **`Fuzz*` 靶点不只在 `test/fuzz`**:`internal/stdlib`(`FuzzPattern`)、`internal/frontend/lex`(`FuzzLexer`)、
  `internal/frontend/parse`(`FuzzParse`)各有自己的靶点,`go-fuzz.sh` 按源码扫描全部跑到。它们的语料(如有)在各自包目录
  的 `testdata/fuzz/` 下——今天只有 `internal/stdlib/testdata/fuzz/FuzzPattern` 存在,lex / parse 目录会在第一个 crasher
  落盘时由 `go test` 创建。写涉及语料路径的脚本时不要假定前缀是 `test/fuzz/`。
- 重 workload 的 crasher(深递归 / 长循环 / 大分配)**不入语料**,走 `test/regression/` 的显式测试;判据与原因见
  [[unreproducible-crasher-triage]]「入库位置的取舍」。
- `scripts/go-fuzz.sh` 靶点发现靠源码扫描 `func Fuzz*`,语料与取证目录都相对包目录寻址,目录挪动不需要改它。

## 取证设施(internal/fuzzforensics)

对外只有两个函数:

- `SetupWorker()`:自己检测 `os.Args` 里的 `-test.fuzzworker`,是 worker 进程才把 fd 2 dup 到
  `fuzz-forensics/worker-<pid>-stderr.log`、`debug.SetTraceback("all")`、打开飞行记录文件;否则直接返回。
  所有错误吞掉——取证绝不能把健康的 fuzz 跑成失败。
- `RecordExec(target, src)`:每次 fuzz 回调在长度检查**之后**调一次,定长 20 KiB 单次 `WriteAt` 覆盖写,零分配。

`test/fuzz/main_test.go` 的 `TestMain` 只做 `fuzzforensics.SetupWorker(); os.Exit(m.Run())`。**它必须留在放
`Fuzz*` 靶点的那个包**:coordinator 把当前测试二进制再起为 worker,`TestMain` 在别的包里就永远不会在 worker 进程
里跑,测试照样全绿而取证静默失效。

## 覆盖率归属

外部测试包自己没有语句,普通 `go test -cover ./...` 会把 `test/` 各包报成 `[no statements]`,它们执行到的根包代
码一行都不记。`scripts/cover.sh`(`make cover` 与 CI 的 test job 都走它)分两次跑:`internal/` 各包用自身 `-cover`;
根包 + `test/...` 用 `-coverpkg=<根包>,<既有非测试源文件又有自己测试的 test/ 包>`(今天是 `test/difftest` 的
`generator.go` 和 `test/conformance/doc.go`,p4 下再加 `test/regression`;列表由
`go list -f '{{if and .GoFiles (or .TestGoFiles .XTestGoFiles)}}…'` 动态得出,新包不必手加)。`test/testutil` 这种只有源文件
没有测试的辅助包**故意不计**:普通 `-cover` 不会给它记分,master 上这段代码是 `_test.go` 里的辅助函数、从不进分母;两份
profile 拼成一份(`go tool cover` 对同位置的 block 按计数求和,这也是多包 `-coverpkg` profile 的常规语义)。

**build tag 必须同时传给 `go list` 和 `go test`**。只在 tag 下存在的包(`internal/gibbous/wasm` 之于 p3、
`internal/gibbous/jit/{amd64,arm64,peroptranslator}` 之于 p4)对不带 tag 的 `go list ./...` 不可见,漏传 tag 的后果是
这些包的单元测试在 CI 里静默不再运行、任务照样绿。`scripts/test-cover.sh`(`make test-scripts` 的一部分)用 stub `go`
钉住这条:`-tags` 出现在 `go test` 参数里时必须原样出现在 `go list` 里。

**不要**改成单次 `-coverpkg=./...`:那会给 `internal/crescent` 的解释器热循环也插桩,实测 `test/regression`
从 12 秒变 400 秒(单个测试 3.9 秒 → 195 秒),叠加 `-race` 后 CI 45 分钟必超时。

## 新增测试往哪放

1. 钉某个 issue 的修复 → `test/regression/issueNNN_*_test.go`。
2. 测公开 API 的行为 → `test/api/`;测 Lua 语言或标准库语义 → `test/language/`;测分层 / 晋升 → `test/tiering/`。
3. 新的 `Fuzz*` 靶点 → `test/fuzz/`,回调里先做长度检查再调 `fuzzforensics.RecordExec`。
4. 两个包都要用的辅助函数 → `test/testutil/`,导出名。
5. `internal/xxx` 自己的单元测试 → 留在 `internal/xxx`,内部测试包。

## 迁移不变量(2026-09-09 已验证)

迁移只改了 package 声明、`runOne` → `testutil.RunOne`(92 处)、`recordFuzzExec` → `fuzzforensics.RecordExec`
(5 处)和语料路径。断言、Test/Fuzz 函数名、build tag、预算、超时、skip 条件一律未动。验证方式:五种 build
(默认 / p3 / p4 / oracle / oracle+p4)下 `go test -list` 出的 Test/Fuzz 名集合与 master 根包逐一相同
(219 / 241 / 235 / 220 / 238);186 个 seed 在新位置全部加载;3 秒真 fuzz 在 `test/fuzz/fuzz-forensics/` 下产出
worker 日志。
