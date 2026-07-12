---
name: 2026-07-12-cgo-oracle-fuzz-round
description: >
  cgo-embedded 官方 Lua 5.1.5 作为 in-process oracle,新增 `FuzzOracleDiff` 差分测试
  (对比两个实现的输出)差分 fuzz + 系统性 argsweep,2026-07-11~07-12,分支
  `feat/cgo-oracle-fuzz`(PR #128,37 commits)。在 P1 crescent 解释器与 PUC Lua
  5.1.5 之间总共扫出 35 处语义分歧,全部修复并把种子/argsweep 用例常驻回归。
  vendored 5.1.5 走 `luaall_c` 单编译单元(承 upstream `etc/all.c` 惯例),
  build tag `wangshu_oracle_cgo && cgo` 门控,默认 build 仍零 cgo。shim 三道
  防护(字节口径 allocator 上限 / `LUA_MASKCOUNT` 指令 hook 协程继承 / 输出
  上限);前置词(共享 Lua prelude)两端都跑,是对称性核心不变式(捕获
  print/io.write / 决定性 stub / 极限错误经 pcall-xpcall-resume 再抛 / 拒绝
  二进制 chunk / pattern 检查 / globals 白名单收窄(在活 wangshu State 上枚举
  使 stdlib 增长自动同步)/ 有序 pairs/next/foreach 迭代)。`FuzzOracleDiff`
  跳过规则只对资源上限与实现常量(语法深度 / 栈溢出 / 复杂度天花板)差异
  返回 LIMIT= 不可比即 skip,其余场景必须结果类等价 + 类型前缀锚定的地址
  规范化后输出逐字节相等 + NaN 符号规范化。本轮五条过程教训里以「invariant
  强度由最严 consumer 定义」的再次出现最重要(stringMeta GC 根、Bash pipefail
  + grep -q 提早关 pipe、fuzz 轮节奏、代码审查抓到的 4 类共性、直接读 lstrlib.c
  / lobject.c / llex.c 比经行为探针快)。
metadata:
  type: reflection
  date: 2026-07-12
---

# 差分 fuzz vs cgo-embedded PUC 5.1.5 一轮扫出 35 处语义分歧(2026-07-11~07-12,PR #128)

> 范围:分支 `feat/cgo-oracle-fuzz`,PR #128,37 commits。新增 `internal/oracle`
> 内嵌 PUC 5.1.5(cgo,build tag `wangshu_oracle_cgo && cgo`),`FuzzOracleDiff`
> 差分 fuzz + 官方 stdlib argsweep 一起扫出 35 处 P1 vs PUC 语义分歧,全部修复
> 并把种子/用例常驻回归。

## 任务

给 wangshu 加一个能与 P1 crescent 解释器同进程做 byte-equal 差分测试的
PUC 5.1.5 oracle,并用一轮 fuzz 把当时能扫到的语义分歧一次收干净。

## 本轮做了什么

1. **`internal/oracle` 包**:把官方 Lua 5.1.5 源码放在 `_lua515/`(下划线目录
   使 go tool 默认忽略),记 sha256;走上游 `etc/all.c` 惯例的单编译单元
   `luaall_c`。cgo 由 build tag `wangshu_oracle_cgo && cgo` 保护,默认 build
   仍然完全零 cgo。shim 三道防护:字节口径 allocator 上限 / `LUA_MASKCOUNT`
   指令 hook(hook 由协程继承)/ 输出上限从共享 Lua prelude 起手抬高。
   verdict 三态 OK/ERROR/LIMIT,其中 LIMIT 表示「不可比」,直接 skip。

2. **共享前置词(prelude)两端都跑,是对称性核心不变式**:
   - 捕获 `print`/`io.write`;
   - 决定性 stub(消随机源与时钟);
   - 把极限触发的错误经 `pcall`/`xpcall`/`resume` 再抛;
   - 拒绝二进制 chunk;
   - pattern 检查(灾难性回溯有界失败);
   - globals 白名单收窄——**在活 wangshu State 上枚举** globals,使 stdlib
     以后长出的新函数自动同步进白名单,不必手工维护;
   - `pairs`/`next`/`foreach` 走有序迭代。

3. **`FuzzOracleDiff` skip 规则**:资源上限(两端预算故意不同)与实现常量
   (语法深度 / 栈溢出 / 复杂度天花板,两端标称值接近但不完全一致,少量
   输入会一端过一端不过)返回 LIMIT → skip;其余场景要求(a)结果类等价、
   (b)类型前缀锚定的地址规范化后输出逐字节相等、(c)NaN 符号规范化后
   相等。

4. **一轮 fuzz + 系统性 argsweep(把 stdlib 每个函数拿去过一遍退化实参
   形式,与 fork-based lua5.1 比对)扫出 35 处分歧,全部修完 + 种子入 corpus**:
   - string 库数字强制:`luaL_checklstring` 语义,数字实参就地 tostring;
   - `__tostring` 原样透传(不再多套一层);
   - coroutine 错误消息里的类型名是 `"thread"`;
   - 未知转义直接透传字面(5.1 而非 5.2 语义);
   - `string.upper` / `string.lower` 走**逐字节 ASCII**——Go 的
     `strings.ToUpper` 会把二进制字符串里的非法字节替成 U+FFFD 使内容损坏;
   - `string.format` 校准:拒 `%F`;`%u`/`%x`/`%o` 走无符号 cast;`%c`/`%s`
     按 C sprintf 走 NUL + strlen 语义;`scanformat` 硬约束 flags ≤ 5、
     width/precision ≤ 2 位——之前自己编的 1 GiB 上限撤掉;`%s` 忽略 `'0'`
     标志;无符号 verb 忽略 `' '`/`'+'`;
   - 常量折叠拒 div/mod-by-zero 与 NaN 结果(PUC constfolding);顺带把
     ±0 常量槽的先到先占规则定下来;
   - 算术 RK 物化顺序:先 o2 后 o1(与 PUC 一致);
   - `tonumber` 重写走 C99 `strtod` 接受面(hex float `0x.8`/`0X.0`/`0x1p4`、
     `inf`/`infinity`/`nan` 词、溢出饱和到 ±inf、hex 整数 fallback 走
     `strtoul` 的 endptr 契约保留 `'x'` 停位);
   - 表构造器字段**按源码顺序**赋值(重构 `TableExpr.Items`——旧的两遍
     codegen 会把覆盖顺序反过来);
   - 字符串真的走**共享 metatable**(`{__index=string}`),`getmetatable("")`
     返回它;**读写两侧**都经 `metaFieldOfValue` 使所有 metamethod
     (包括写路径的 `__newindex`)真的能触发;
   - `load` 只吃 reader 函数(字符串接受面是 5.2 才有的);
   - `os.time` 表协议;
   - `unpack` 范围实参经 C `int` 收窄(NaN → int64 min → 低 32 bit 为 0)。

5. **过程侧关键收获**:
   a. **stringMeta 的 GC 根**——新加的 stringMeta 表只被 Go 侧的 State 字段
      持有,`TestGCStress_AllocHeavy` 立刻抓到 use-after-free。任何 State
      级长期持有的 GCRef 都必须加入 `visitExtraRefs`——这条前已存在(承
      公共 API 增量交付的 GCRef 接根契约条款),本轮是又一实证。
   b. **Bash 陷阱**:`go test ... | grep -q` 在 pipefail 下会因 grep 命中
      即退出让上游收到 SIGPIPE 死掉——每一轮探针的第二次及以后循环迭代
      全都因此挂掉。解药是**先落盘再 grep**;并对循环里的探针加
      `</dev/null` 使其不吞外层 `while read` 的 stdin。
   c. **Fuzz 轮节奏**:后台跑 45~240 分钟一轮,每次落一个分歧就当场修 +
      重放 corpus + 再开新一轮。corpus 逐步长成常驻回归集。CI 上的
      arm64 oracle-smoke 抓到过一个本机多轮没抓到的变体(`"% 00X0"`
      这类格式)——不同平台探索的 mutation 路径不一样。
   d. **代码审查(本地 `.code-review/` + PR bot)抓到的四类**:
      1. PUC folding 修完使 `TestI117` 的 `SpecForLoopHits>0` 断言失效
         ——模板级 NaN 义务挪进 emitter 测试(用真的 NaN 位),源码级
         测试反过来断言 hits **不**增长;
      2. 字符串 metatable 第一版只经 `__index`——改经 `metaFieldOfValue`
         使 `__add`/`__tostring` 等都触发,第二轮又发现写路径的
         `__newindex` 仍被绕过;
      3. `go-fuzz.sh` 探针把编译失败静默 skip 掉(等于 oracle-smoke 拿绿
         灯而不做事)——探针失败改为致命 + 加必需目标断言;
      4. `0x` 前缀的地址规范化没有锚定,盖住了真正的 hex 输出分歧——
         改成按类型前缀锚定。
   e. **「invariant 强度由最严 consumer 定义」再次出现**——PUC 的语义由
      C 实现的精确行为定义(`strtod`、`sprintf`、`strtoul` endptr 契约),
      不由手册文字定义。直接读 `lstrlib.c` / `lobject.c` / `llex.c` 比
      用探针试出来更快也更准。
   f. **用户口径提醒**:代码注释统一英文(本轮批量转过一次);pre-push
      hook 不能把 non-fast-forward 自动升成 force(经 `PREPUSH_ALLOW_FORCE=1`
      门控);push 工作流 8 条纪律已入用户 memory。

6. **文档同步**:
   - `README.md` correctness 层加第 5 条(cgo oracle 差分 fuzz);
   - `docs/design/engineering.md` §3.2 记 nightly / PR gate 接线;
   - `tmp/cgo-oracle-fuzz.md`(未入库,本地临时草稿)。

## 期望与实际

- 期望:cgo oracle 装上后先跑几分钟 smoke,预计有个位数分歧,一天内收干净。
- 实际:第一轮就跑出 3 处;之后 fuzz + 系统性 argsweep 累计扫出 35 处
  语义分歧(P1 与 PUC 5.1.5 之间),分布在 stdlib、词法、前端 codegen、
  值语义(string metatable / 常量折叠)各处;两天内每一条都定位到 C
  代码原点、修好、种子入回归。

差距的来源是**参照系升级**:此前 P1 只与自身生成的随机脚本 + 手写特性
探针互证,没有一个能在同进程里精确复刻 PUC 逐字节输出的 oracle。cgo
oracle 一装上,以前没被差分覆盖的 stdlib 边界形式立刻成片浮出来。

## 教训

### 教训 1(参照系一升级,潜伏分歧会成片浮出——预算按数量级留)

在此之前只有子进程 `fork-lua5.1` 差分,交互慢、只能跑手写脚本、没有
in-process byte-equal 校验;cgo oracle 上线后同一轮 fuzz + argsweep 就
把 35 处此前一直不被差分看见的分歧一次挑出来。教训:**估算 oracle
升级的收益要按「参照系覆盖面」而非「已知 bug 数」估**——已知 bug 数
反映的是旧参照系能看见的部分,和新参照系能看见的往往差一个数量级。

**How to apply**:接一个「上更强 oracle / 新差分维度」类任务前,预算
要给「大量分歧陆续掉出来」留时间,而不是按当前 bug tracker 里挂着
几个 issue 估。

### 教训 2(PUC 语义由 C 代码定义,不由手册定义——直接读源码比探针快)

`string.format` 的 flags 数量上限、width / precision 位数上限、`%s` 忽略
`'0'`、无符号 verb 忽略 `' '`/`'+'`、`tonumber` 走 `strtod` + `strtoul`
endptr 契约、常量折叠拒 div/mod-0 与 NaN 结果——这些细节全在 C 源码
里明确写着,手册要么没写要么写得比实现松。本轮反复出现的模式是:
用探针猜半小时找不到边界,回头读 `lstrlib.c` / `lobject.c` / `llex.c`
五分钟就写清楚。

**Why**:差分对手是 C 实现本身,不是文档。C 代码里 `sprintf` 走宿主
libc、`strtod` 走宿主 libc,连宿主 libc 的边界都是 PUC 语义的一部分。
只有源码里明确写着「hard limit」的地方才有稳定语义可差分。

**How to apply**:任何「与 PUC byte-equal」的分歧,先在 `_lua515/`
里 grep 对应实现,把 C 侧的接受面/拒绝面/边界值写进 wangshu 侧,再
用 fuzz 校验。别在探针里试常数。

### 教训 3(State 级长持 GCRef 必须接根,再一次)

新加的 `stringMeta` 表被 State 字段持有——`TestGCStress_AllocHeavy`
立刻 UAF。修法是把它加进 `visitExtraRefs`。这条不是新纪律,是公共
API 增量交付纪律里 GCRef 接根契约的又一实证——**这次值得记的是
GC stress 测试能在合并前就抓到**,不必等公共 API 露出。任何在
State 上加长持字段的改动,`TestGCStress_*` 就是最便宜的兜底。

### 教训 4(测试 harness 里的静默 skip / 静默 pipe 关闭是持续雷区)

`go-fuzz.sh` 探针把编译失败静默 skip 掉,使 oracle-smoke 长期拿绿灯
而不做事;`| grep -q` 因 SIGPIPE 提早关 pipe 使循环里第二次及以后
迭代的 `go test` 全挂——两者都是 harness 层面「路径没走通但表面
绿」的实例,是 [[prove-the-path-under-test]] 家族「静默替身」在
shell 层的对应物。

**How to apply**:探针脚本的默认写法是「失败即致命 + 必需目标断言」
+「先落盘再 grep,不写 `| grep -q`」+「循环内探针加 `</dev/null`
不吞外层 stdin」。

### 教训 5(review 抓到的四类都是「快路径覆盖不到但慢路径能看到的形式」)

四类共性:模板级测试的义务与源码级测试的义务不同、语义要经统一
入口而不是绕过、探针失败要当红旗、规范化要有锚点。共同规律是:
一个原本走慢路径的语义分歧被 inline / metatable 分派 / 归一化
之类的机制包一层之后,原来的测试不一定继续覆盖,review 时按
「新写的 fast path 是不是把某个已存在的 slow path 义务落下了」
逐条问一遍。

### 教训 6(argsweep 与 fuzz 的关系:形式生成器 vs 语义 mutation)

fuzz 走随机 mutation,擅长把已经写出来的调用形式扭出边界值(数字
NaN、极大整数、格式串带奇怪 flag);argsweep 按 stdlib 每个函数
枚举退化实参形式(nil / 空表 / 字符串代数字 / 数字代字符串 /
超范围 / 空字符串),擅长把没被人写过的调用形式一次全过一遍。
两者互补——fuzz 抓「值层面」的边界,argsweep 抓「形式层面」的
覆盖面。本轮 35 处分歧里 fuzz 抓 20 处、argsweep 抓 15 处,是
两条独立探索通道的实证。

## 缺失的文档或信号

- 「shell 探针默认写法」目前散落在多个反思里(GHA `eval` 剥引号、
  `pgrep` 轮询、本轮 pipefail + grep -q + heredoc 陷阱等),没有一
  篇集中的 shell 探针 checklist。教训 4 会重复出现直到有集中登记。
- 「oracle 升级会成片抛出潜伏分歧」的预算纪律(教训 1)以前没有
  单独条款;`perf-optimization-workflow` 里的 profile-先行讲的是
  实现前预判被推翻,和这条不完全同族。

## Promotion 候选

- **教训 1**(参照系升级一次到位 → 分歧成片浮出):**首次样本暂留
  观察**,若下一轮再上更强 oracle 或差分维度还是抛一堆,可作独立
  guide「oracle 升级的收益按覆盖面而非已知 bug 数估」或并入
  [[unreproducible-crasher-triage]] 的「决定投入多少诊断硬化」邻接
  段。
- **教训 2**(PUC 语义由 C 代码定义):**首次以独立教训形式出现,
  建议留反思**——它是 wangshu 与 PUC 差分场景的具体手法,通用性
  没到 guide 级。反引 [[design-claims-vs-codebase-physics]] 的「设计
  稿主张须对本码库 physics 重新验证」作对偶(那边讲设计稿主张 vs
  本码库 physics,这边讲 PUC 手册文字 vs PUC C 源码)。
- **教训 3**(State 级长持 GCRef):已是 [[public-api-incremental-delivery]]
  guide 第 2 条与 [[embedding-contract]] 不变式段的既定条款,本轮
  是 GC stress 测试**在合并前**兜住的又一实证,不升 guide,memory
  内反引即可。
- **教训 4**(shell 探针静默 skip / pipefail + grep -q):**接近阈值**
  ——已有多轮反思(GHA `eval` 剥引号、`pgrep` 轮询、本轮)提到
  shell 探针的默认写法。建议开一篇独立小 guide「shell 探针默认
  写法 checklist」,或作为 [[prove-the-path-under-test]] §6/§7 的
  shell 层对偶补一节;本轮先落 memory + 反引。
- **教训 5**(review 抓到的四类):首次样本,暂留观察。若下一轮
  review 再抓到同族,可作 [[prove-the-path-under-test]] 的「新加
  快路径时,列出被绕过的既有慢路径义务」条款。
- **教训 6**(argsweep 与 fuzz 互补):首次样本,暂留观察。若
  P4 / P5 后端接入面扩展时再实证一次,可作差分测试策略 guide
  的独立小节。

## 触发场景

- 装一个新 oracle / 新差分维度前——按覆盖面而非已知 bug 数估
  预算(教训 1);
- 与 PUC 5.1.5 byte-equal 的分歧调查——先读 `_lua515/` 里对应
  C 实现,再决定修法(教训 2);
- 在 State 上加长持字段(尤其 GCRef 类)——同 PR 加 `visitExtraRefs`
  接根,靠 `TestGCStress_*` 兜底(教训 3);
- 写 shell 探针 / 循环内跑 `go test`——避 `| grep -q`(pipefail 下
  SIGPIPE)+ 循环内加 `</dev/null` + 探针失败致命 + 必需目标断言
  (教训 4);
- review 一个新加的 fast path / inline 快路径——按「被绕过的既有
  慢路径义务」逐条对(教训 5);
- 扫某个大接受面(stdlib / 前端 codegen)时——fuzz + argsweep
  一起上,一个抓值层边界一个抓形式层覆盖(教训 6)。

## 关联

[[public-api-incremental-delivery]](GCRef 接根,教训 3 是其又一
实证)· [[prove-the-path-under-test]](教训 4/5 是其 shell 层
与 review 层的邻接对偶)· [[design-claims-vs-codebase-physics]]
(教训 2 是其「文档 vs 实际」对偶——手册文字 vs C 源码)·
[[cross-backend-semantic-fix-sweep]](本轮 string metatable 经
`metaFieldOfValue` 统一入口的做法,是「多条独立 emit 通道」在
runtime 分派层的对应,把读/写两侧的分派点收成一份)·
[[2026-07-11-issue125-return-freereg-round]](本轮修的表构造器
字段源码序 + 算术 RK 物化顺序,与那轮的前端 codegen 存量 bug
同域;两轮都靠 `luac -l` 逐指令对照法定位)· PR #128 ·
`internal/oracle/_lua515/` · `internal/oracle/shim.go` ·
`FuzzOracleDiff` · `README.md` correctness §5 ·
`docs/design/engineering.md` §3.2
