---
name: 2026-07-13-nightly-concat-oom-and-format-hash-round
description: >
  2026-07-13 每日 nightly-diff-fuzz 巡检轮(分支 fix/nightly-concat-oom,PR #131,
  尚未合入,CI 全绿 + bot APPROVE 无阻塞问题)。处理两件事:① nightly p3/p4 两条腿
  crasher #127/#130 的诊断与结构性修复;② PR #131 自己带的 oracle-smoke CI(30s
  FuzzOracleDiff)立刻撞出并顺手修掉的 `%#X` 零值前缀分歧。第一部分把两份 crasher
  corpus 的死亡画像与 issue #123 同族(几千万 execs 后无声 hung / 精确重放干净)判定
  为二次方 concat 风暴引起的进程级资源耗尽,而非 input 决定的 VM bug;真根因是同族
  三个老 harness(FuzzCompileRun / FuzzAutoPromote / FuzzP4ForceAllPromote)缺
  MaxArenaBytes 帽,而上周 PR #128 上线的 FuzzOracleDiff 出生就带这个帽,新防护上线
  时没有横向扫兄弟 harness。修法给三个老 harness 补 MaxArenaBytes: 64 << 20 + 差分
  豁免从只识 budget 扩为通用 isResourceLimitErr(匹配 "instruction budget exceeded"
  或 "internal VM panic: arena:")+ 两份 corpus 入库常驻回归。第二部分把 stringFnFormat
  的 %u/%x/%X/%o 分支从「改写 spec 再委托 Go fmt」路线整段换成手写 cUnsignedFormat
  直接实现 C 语义——这是同一个 site 在 oracle diff fuzz 上第三次被打穿(第一次
  `%100X` 宽度、第二次 `% 00X0` 旗标、这次 `%#X` 零值前缀),Go fmt 与 C printf 在
  '#' 上的分歧不止一处,继续打补丁不收敛。核心可复用教训:①「改写输入再委托宿主
  语言标准库」适配外部 C 语义是不收敛的补丁路径,第 N 次被打穿时应换手写直接实现,
  与 [[2026-07-12-i123-deadline-to-package-timeout-round]]「调常量修 flake 不收敛
  → 换机制」是同族决策模式的第 2 实例;② 同族 harness 的防护不对称——新 harness
  出生带的防护应在上线时立刻横向扫兄弟 harness,与 [[cross-backend-semantic-fix-sweep]]
  的「修复心理边界停在当前站点、真实边界是全部同类站点」同构但对象是测试 harness;
  ③ 资源限制类差分豁免要按「类」豁免而不是按「具体错误」豁免。
metadata:
  type: reflection
  date: 2026-07-13
---

# nightly p3/p4 crasher 处置 + oracle format '#' 分歧收敛(2026-07-13,PR #131)

> 范围:分支 `fix/nightly-concat-oom`,PR #131(未合入,CI 全绿 + bot APPROVE 无
> 阻塞问题)。处理 issue #127(p3 腿)+ #130(p4 腿)两个每日 nightly crasher,
> 顺手修掉 PR #131 自己的 oracle-smoke CI 立刻撞出的 `%#X` 零值前缀分歧。

## 任务

- 处理 nightly-diff-fuzz 2026-07-13 巡检报的两条腿 crasher #127(p3)+ #130(p4);
- PR #131 自己的 oracle-smoke CI(30s FuzzOracleDiff 冒烟)撞出 `print(string.format
  ("%#X",0))` 的 P1 vs PUC 分歧(P1 输出 `0X0`,PUC 输出 `0`),同一站点第三次被
  打穿,顺手把这个 site 结构性修一次。

## 期望与实际

### 第一部分 nightly crasher

- 期望:两份 corpus 落盘,精确重放应能立即复现 VM 侧根因。
- 实际:两份 corpus 本地精确重放全部干净(#130 的 p4 腿 45s 正常跑完,#127 的
  p3 腿 8s),worker 死亡画像是「几千万 execs 后 hung or terminated unexpectedly、
  无 panic 输出」——与 issue #123 完全同类的进程级资源耗尽签名,符合
  [[unreproducible-crasher-triage]] 的分界判据。深查 corpus 内容后发现两者都是
  二次方 concat 风暴(`out = out .. f(i)` 热循环),实测 #130 corpus 在未封顶的
  默认 2 GiB arena 下,单次执行冲到 arena 报 `arena: cannot grow to 2147508352
  bytes`、Go 堆 13 GiB(grow 翻倍时新旧 backing 同时存活,死 concat 垃圾在 GC
  阈值间隙累积),step budget(1<<20)来不及先触发;4 个并行 fuzz worker 把它
  放大成进程级 kill。真根因是同族三个老 harness(FuzzCompileRun / FuzzAutoPromote
  / FuzzP4ForceAllPromote)缺 MaxArenaBytes 帽,上周 PR #128 上线时给
  FuzzOracleDiff 加了这个帽,但新防护上线时没有横向扫兄弟 harness。

### 第二部分 oracle format '#'

- 期望:再打一发补丁把 `%#X` 零值前缀补上,与前两次(`%100X` 宽度上限、`% 00X0`
  旗标)一样处理。
- 实际:动手前把 Go fmt 与 C printf 在 '#' 上的分歧列了一遍——除了这次的零值前缀
  省略,`%#08X` 补零要在前缀内侧、`%#o` 强制首位 0、`%#.0o` 零值 C 打 "0" Go 打
  空——分歧集合是开放的且四条都能被 fuzz 撞到。「把 spec 改写几笔再委托 Go fmt」
  的路线每次只能补上被撞到的那一个分歧,继续打补丁不收敛。修法转向整段换成手写
  cUnsignedFormat 直接实现 C 语义。

## 常量演进史(证明「改写 spec 再委托 Go fmt」路径不收敛)

- 第一次(过去某轮):`%100X` 宽度上限——对齐 PUC scanformat 的 2 位宽度硬限;
- 第二次(过去某轮):`% 00X0` 旗标——用 `bytes.ReplaceAll` 从 spec 里剥掉 ' '
  和 '+' 两个字符再喂给 Go fmt;
- 第三次(本轮):`%#X` 零值前缀——如果继续沿路径应该再加一条剥掉 '#' 或者附加
  `if v==0` 的补丁,但这样只处理了本次撞到的这一档;
- 定下来的修法:整段换成手写 cUnsignedFormat,直接实现 C99 printf 规则(自解析
  flags/width/precision,strconv 出 digits,精度零扩展、'#' 前缀规则、C 补零规则)。

三次修的都是「Go fmt 与 C printf 的又一处分歧」;分歧集合是开放的(两个实现各自
演进),而手写 renderer 把语义收敛为封闭集合(C99 printf 规则一页写完)。

## 结构性修法

### 第一部分 nightly crasher

- 首个 commit c574f24 一带:给全部 fuzz harness 的 State 加 `MaxArenaBytes:
  64 << 20`——fuzz_test.go 的 FuzzCompileRun / fuzz_auto_test.go 的
  FuzzAutoPromote / fuzz_p4_test.go 的 FuzzP4ForceAllPromote 三处补齐,与
  FuzzOracleDiff 上周(PR #128)出生就带的这条防护看齐;
- 差分豁免从只识 budget 扩为资源限制类:新 helper `isResourceLimitErr` 匹配
  "instruction budget exceeded" 或 "internal VM panic: arena:",因为各 tier
  分配点不同,临界 input 可能只在一条腿撞帽;
- 两份 corpus 入库 `testdata/fuzz/FuzzAutoPromote/`,封顶后轻量(同 shape ~1s
  内以 arena 帽错误终止);
- 验证:三 build variant 全量测试绿、lint 0 issues、FuzzCompileRun 60s 419 万
  execs / FuzzAutoPromote 60s 113 万 execs 冒烟干净。

### 第二部分 oracle format '#'

- 修法 commit 643c6cb:stringFnFormat 的 %u/%x/%X/%o 分支整段换成手写
  cUnsignedFormat 直接实现 C 语义——自己解析 flags/width/precision,strconv
  出 digits,精度零扩展(零值 + 显式零精度 → 空串)、'#' 前缀规则、C 补零规则
  ('-' 压过 '0';给了精度则 '0' 旗标被忽略;unsigned 忽略符号旗标)。宽度/精度
  本就被上游 2 位硬限封顶,无 OOM 面;
- 验证:41 个覆盖前缀/宽度/精度/旗标交互的用例与本机 PUC lua5.1 逐字节相等;
  三 variant 测试绿;90s oracle fuzz 冒烟(21.6 万 execs)干净;corpus 入库
  `testdata/fuzz/FuzzOracleDiff/8605c41c6420e3ba`;bot 增量审查 APPROVE,逐条
  核对了重解析逻辑与上游 spec 构造的一致性、C 语义正确性、无 OOM、无未用 import。

## 核心教训

### 教训 1(头条:「改写输入再委托宿主语言标准库」适配外部 C 语义是不收敛路径,第 N 次被打穿时换手写直接实现)

同一个 site(stringFnFormat unsigned 分支)三次被 oracle diff fuzz 打穿,每次修
的都是「Go fmt 与 C printf 的又一处分歧」。分歧集合是开放的——Go fmt 与 C printf
各自沿自己的规范演进,谁也不承诺兼容对方——所以「把 spec 改写几笔再委托宿主
标准库」这条路径的每次修复只覆盖「已被撞到的分歧」而无法枚举「全部分歧」。手写
renderer 把语义收敛为封闭集合:C99 printf 规则一页写完,把该规则直接写进代码
就再也不必追 Go fmt 的行为。

**Why**:适配层的正确性依据是「宿主标准库的行为 + 我加的改写规则 = 目标外部
语义」,只要宿主标准库任一分支上的行为与外部规范有一处分歧,就要在改写规则里
加一条对应处理;两个实现的分歧集合不由适配层控制,继续补丁只能追已知不能封闭
未知。手写直接实现的正确性依据换成「目标外部规范本身」,规范是一份稳定文档,
可以一次读完写完。判据:适配层每次修复只覆盖「已被撞到的分歧」而无法枚举「全部
分歧」时,路径不收敛。

**How to apply**:在做「改写输入再委托宿主语言标准库」适配外部 C 语义时,如果
同一站点被 fuzz / 差分测试 / 用户 bug 报打穿到第二次以上,不要再补下一发,而是
换成手写直接实现——把外部规范写进代码。判据是把两个实现的已知分歧列一遍:如果
列出来发现「除了本次撞到这一档,还有若干档也可能撞」,就换机制。

这与 [[2026-07-12-i123-deadline-to-package-timeout-round]] 的「调常量修 flake
不收敛 → 换机制」是同族决策模式:被同类失败打穿 ≥2 次后,先问「这条路径能收敛
吗」,答案是否就换机制而不是再补一发。两轮的具体场景不同(那轮是测试裁判机制、
本轮是语义适配层),但决策模式相同——都是「用可数近似逼一个不可数目标」的路径
不收敛,换成「直接实现目标」才收敛。

### 教训 2(同族 harness 的防护不对称——新 harness 上线时立刻横向扫兄弟 harness)

FuzzOracleDiff 上周(PR #128)上线时出生就带 MaxArenaBytes 帽——它的设计文档
`.llmdoc-tmp/cgo-oracle-fuzz.md` 明确考虑过资源问题——但同仓更老的三个 fuzz
harness(FuzzCompileRun / FuzzAutoPromote / FuzzP4ForceAllPromote)没有这个帽。
#127/#130 两个 nightly crasher 本质上就是这个不对称欠下的:如果 PR #128 给
FuzzOracleDiff 加帽时顺手扫了三个老 harness,这两个 issue 不会发生。

**Why**:防护/修复的心理边界停在「当前文件」,而真实边界是「全部同类站点」。
这与 [[cross-backend-semantic-fix-sweep]] 的「修同一语义 bug 时枚举全部后端 ×
通道」同构,但对象是测试 harness 而非 emit 通道。新增或改写测试 harness 时,
harness 上带的资源上限、超时、错误豁免等防护是一份「隐性契约」,同族 harness
应共享同一份契约;单一 harness 独享新契约,其他老 harness 的资源暴露面就与它
之间对称性被破坏,并在与该防护相关的 fuzz 上放大成 crasher。

**How to apply**:给某个 fuzz / 差分测试 harness 加新防护(资源上限、超时、
豁免规则、错误恢复路径等)时,同 PR 内 grep 所有兄弟 harness 是否需要一样的
防护——判据「兄弟 harness 是否存在同类暴露面」,不是「兄弟 harness 是否碰巧
撞过」。grep 手法:按 harness 类别列表(fuzz_*_test.go / diff_*_test.go 等),
对每个 harness 检查新加防护相关的 State option / 错误豁免规则是否需要同步。

### 教训 3(资源限制类差分豁免按「类」豁免,不按「具体错误」豁免)

差分测试豁免规则以前只匹配 "instruction budget exceeded",本轮改为通用
`isResourceLimitErr`(匹配 "instruction budget exceeded" 或 "internal VM panic:
arena:")。原因是 tier 间分配点不同:临界 input 在 P1 可能先撞 step budget、在
P3 wasm 段内可能先撞 arena 帽,两条腿产生的错误字符串不一样但都是资源尽头;
豁免只认 budget 字符串就会把资源竞速误判成 tier 分歧。

**Why**:差分测试的核心不变式是「同 input 两 tier 应产生同结果」,资源限制是
tier 无关的物理量而不是语义量。「同 input 在 tier A 撞资源尽头、在 tier B 也
撞资源尽头,但撞的尽头种类不同」不构成语义分歧,应作为豁免类而非豁免个体。

**How to apply**:写差分测试豁免规则时,把资源尽头按「类」组织(instruction
budget / arena cap / stack overflow / 输出上限等),用统一 helper 判定;不要
用字符串精确匹配去区分错误个体——tier 间分配点差异是正常物理事实,豁免规则的
表达应与 tier 无关。

## Promotion 候选

- **教训 1**(「改写输入再委托宿主标准库」适配外部 C 语义是不收敛路径 → 换手写
  直接实现)与 [[2026-07-12-i123-deadline-to-package-timeout-round]] 的「调常量
  修 flake 不收敛 → 换机制」构成「不收敛路径 → 换机制」**第 2 实例**(那轮是
  测试裁判机制、本轮是语义适配层,家族形式不同但决策模式相同)。**再现一次
  建议升 guide**,暂名 `non-converging-patch-path-detection`,或并入某决策类
  guide 作「不收敛路径识别」新节。判据统一为:被同类失败打穿 ≥2 次时问路径是否
  收敛,枚举「能撞到但还没撞到」的档次,若集合开放就换机制。首次以第 2 实例
  形式出现,memory 内反引 [[2026-07-12-i123-deadline-to-package-timeout-round]]
  作实证,不即时升 guide。
- **教训 2**(同族 harness 的防护不对称)是 [[cross-backend-semantic-fix-sweep]]
  的 harness 侧对偶(那篇管修复的心理边界停在当前后端 × 通道,本条管防护的心理
  边界停在当前 harness)。**第 1 个 harness 侧实例暂留观察**,可作该 guide
  「适用面延伸」候选,若后续再出现「给一个 harness 加防护但漏了兄弟 harness
  一直到 fuzz 撞到才补」的第二实例即可升 guide 正文一节。
- **教训 3**(资源限制类差分豁免按「类」豁免)首次样本,暂留观察。若后续再出现
  「豁免规则精确匹配错误字符串把 tier 间分配点差异误判成分歧」的第二实例,可
  升 guide 或并入差分测试相关章节。

## 触发场景

- 做「改写输入再委托宿主语言标准库」适配外部 C 语义(或类似「委托型适配」)时,
  同一 site 被同类失败打穿到第二次以上,想再补下一发之前(教训 1:先问路径是否
  收敛、能撞到但还没撞到的档次集合是不是开放的,是就换手写直接实现);
- 给某个 fuzz / 差分测试 harness 加新防护(资源上限、超时、豁免规则等)时(教训
  2:同 PR 内 grep 兄弟 harness 是否需要一样的防护);
- 写差分测试豁免规则时(教训 3:资源尽头按「类」组织统一 helper 判定,不按字符
  串精确匹配区分个体);
- nightly / CI 报 crasher 但本地重放不出、几千万 execs 后无声死时(承
  [[unreproducible-crasher-triage]]:先做版本核对 + 分诊判定 input 决定的 VM
  bug 还是进程级资源耗尽,后者优先加/横扫资源帽把无声外部 kill 转成带栈的
  runtime OOM fatal,而非硬编修不存在的 VM bug)。

## 验证

- 三 build variant 全量测试绿,lint 0 issues;
- FuzzCompileRun 60s 419 万 execs、FuzzAutoPromote 60s 113 万 execs、
  FuzzOracleDiff 90s 21.6 万 execs 冒烟干净;
- 41 个覆盖前缀/宽度/精度/旗标交互的手写用例与本机 PUC lua5.1 逐字节相等;
- 三份 corpus 入库(`testdata/fuzz/FuzzAutoPromote/` 两份 + 
  `testdata/fuzz/FuzzOracleDiff/8605c41c6420e3ba`);
- PR #131 CI 全部检查绿,bot APPROVE 无阻塞问题,应用户要求停在合入前等 review。

## 后记(同日追加,PR #132):build tag 加法性打爆 nightly p1 腿预算

巡检收尾统计连续干净轮数时发现 2026-07-12 傍晚两轮 nightly run 的 conclusion 是
`cancelled`(p3/p4 腿 success、p1 腿 cancelled),不是 failure 所以没触发
`if: failure()` 的 triage、没有 issue,属于静默损失。查 p1 腿分步耗时:
rolling-seed 38 分钟 + GC-stress 1 分钟 + native go-fuzz 步骤 **3 小时**(本该
只跑默认 build 的 4 个目标 × 45m——这一步没问题)+ oracle-diff 步骤从 00:31 跑
到 02:45 被 350m job timeout 掐断。根因:oracle-diff 步骤调 `go-fuzz.sh` 只传了
`wangshu_oracle_cgo` tag,而 **build tag 是加法**——该 tag 下默认 build 的全部
fuzz 目标照样可编,脚本的「自动发现全部 `func Fuzz*`」把 5 个目标全部按 45m 重
跑一遍(4 个是纯重复预算),叠加起来必然冲破 350m。修法(PR #132):给
`go-fuzz.sh` 加可选第三参数 `target_regex` 精确过滤目标名,显式过滤却零匹配时
exit 1(笔误/目标改名不该静默变空扫);nightly oracle-diff 腿与 PR 门禁
oracle-smoke 的 fuzz 步骤都传 `FuzzOracleDiff`。

这是教训 2「同族 harness 防护不对称」的一个变体:「自动发现」型脚本的便利默认
(全目标扫描)在「tag 是加法」的物理事实下,对 tag 门控的调用方是隐性的预算
放大器——调用方以为「传了 oracle tag 就只跑 oracle 目标」,实际语义是「跑
oracle tag 下能编的一切」。**How to apply**:凡是「按 tag 选目标」的调用意图,
必须用显式目标过滤表达,不能依赖 tag 隐式缩小目标集;写「自动发现 + 逐个跑」
类脚本时,给调用方留一个显式过滤参数,并让「显式过滤零匹配」响亮失败。另:
conclusion=cancelled 的 run 不触发 failure 类 triage,巡检统计连续轮数时
cancelled 与 failure 同样要点开看原因,不能只把 failure 当信号。

## 关联

[[unreproducible-crasher-triage]](本轮第一部分承此 guide 的分界判据:落盘 input
精确重放干净 + 几千万 execs 后无声死 = 进程级资源耗尽嫌疑,处置 = corpus 入库
+ 诊断硬化,不硬修不存在的 VM bug)· [[cross-backend-semantic-fix-sweep]](本轮
教训 2 的对偶 guide:那篇管修复的心理边界停在当前后端 × 通道,本条管防护的心理
边界停在当前 harness,harness 侧首次实例暂留观察)·
[[2026-07-12-i123-deadline-to-package-timeout-round]](本轮教训 1 的对偶前序:
那轮是测试裁判机制的不收敛路径 → 换 harness 包级 timeout,本轮是语义适配层的
不收敛路径 → 换手写直接实现,同族决策模式的第 2 实例)· issue #127 · issue
#130 · PR #131 · PR #132(后记:build tag 加法性打爆 nightly p1 腿预算,
`go-fuzz.sh` 加 target_regex 过滤)· PR #128(上周新增 FuzzOracleDiff 时带
MaxArenaBytes 帽,同族老 harness 未同步)· commit c574f24 · commit 643c6cb ·
`testdata/fuzz/FuzzAutoPromote/`(两份 concat 风暴 corpus 入库) ·
`testdata/fuzz/FuzzOracleDiff/8605c41c6420e3ba`(`%#X` 零值 corpus 入库)
