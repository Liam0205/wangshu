# Guide:设计稿主张须对本码库 physics 重新验证

> 适用:把设计稿热路径上的抽象记号(`(call $x)`)、固定 token(base/指针/句柄/视图)、或成本主张照搬到实现之前——尤其每指令必经的快路径、跨层/跨调用存活的值。**或处理「设计稿/task 描述/stub 注释承诺/外部依赖现状」类前序快照在事实变更后失效**(§5「时间维度」)。**或脚本/工具链/包依赖在跨 OS / shell / runtime 版本物理环境间静默挂**(§6「空间维度」)。P3 翻译全程复发,P2 编译层同理,P4 method-JIT 闸门翻面期同理,CI 矩阵扩平台时同理。
> 来源:`memory/reflections/2026-06-13-issue8-boundary-cost-round.md`(成本归类)+ `memory/reflections/2026-06-14-p3-pw5-table-ic-round.md`(边界成本预算)+ `memory/reflections/2026-06-14-p3-pw6-crosslayer-call-round.md`(段重定位 UAF)+ `2026-06-14-p3-pw7-pw4b-closure-tforloop-round.md`(难点过期)+ `2026-06-16-vs0e-varargs-stack-underflow-round.md`(调研先于实现)+ `2026-06-24-p4-doc-review-round.md`(外部依赖现状过期)+ `2026-06-30-pr27-f3-3b-darwin-arm64-execute-roundup.md`(sentinel 注释承诺与闸门状态解耦)+ `2026-06-30-pr28-f3-3c-tri-platform-matrix-ci.md`(bash 3.2 vs 4+ / actions/cache symlink / homebrew 包政策 跨 OS 物理环境差异)——独立实例聚合为一个判断框架。

设计稿表达的是**语义意图**,用抽象记号写在纸上;它**不携带本码库的物理不变式**。三次了:把设计稿热路径上的一条主张/记号忠实誊写到加速层,本会产出一个 bug 或一处死优化——因为设计稿对某条 wangshu 专属的物理事实是盲的(边界成本、arena 段重定位、GC 根可达性……)。**热路径上的抽象记号在实现前,必须逐条对照本码库 physics 重新推导,而不是照抄伪码。** 这条横跨**性能**(誊写出死优化)与**正确性**(誊写出 UAF)两面,故单独成 guide,不并入 [[perf-optimization-workflow]]。

## 1. 边界成本预算——热路径上的 `(call $x)` 能否塌成 inline

设计稿 WAT 伪码里写成 `(call $helper ...)` 的快路径节点,是**记法不是承诺**:`$helper` 只表达「这里要做 X 语义」,不等于「这里要发一次真实跨层调用」。**PW0 spike 实测一次 gibbous→host imported 调用约 ~143ns**——若快路径上(尤其每指令必经的 IC 快路径)真调一次助手,「跳过哈希」省下的几个 ns 当场被边界成本吞光,整个 inline 加速立项归零。

**实例(PW5)**:`02-translation.md` §3.4 把 `$ic_slot_load`/`$ic_key_match` 写成助手调用形态。正解是全 inline `i64.load`——table 活在 arena = wazero linear memory,`value.GCRefOf(v)`(低 48 位)**就是**字节偏移,`SNAP_INDEX` 是编译期立即数 ⟹ 所有槽 offset 都是常量,助手退化成几条 load。

**判据**:任何标在热路径的助手记号,实现前拿 ~143ns/次的边界成本预算重过一遍——它能不能塌成几条 inline load?能,就**必须** inline,否则该 opcode 的整个加速失去意义。

## 2. arena 段重定位——跨层/跨调用存活的 base/指针/视图,底层段会不会被 grow 搬走、谁能刷新

本码库的物理事实:**值栈与对象世界共享自管 arena,任意分配触发的 grow 会把段在 arena 里重定位**(改写底层基址)。设计稿把一个热路径值跨层画成「入口一次性收到、全程不变」的固定 token 时,这条不变式不在它视野里,却让那个「固定 token」在落地时变成悬垂指针。

**实例(PW6)**:`04-trampoline.md` §2.2 把 `$base`(linear memory 字节偏移)画成 gibbous wasm 函数入口锁定、全程不变。但 gibbous 帧经 `h_call` 调更深 Lua 帧时,嵌套 `growStack`(`internal/crescent/state.go`)把值栈段在 arena 重定位、改写 `th.stackBaseW`,返回后陈旧 `$base` 指向已 Free 旧段 = **UAF**。解法:`h_call`/`h_tailcall` 返回**重算后的新 base**(i64,负哨兵表错),CALL 翻译 `local.tee`→负则 `return 1` 冒泡/否则 `local.set $base` 刷新。

**判据**:这个 token 在它存活的窗口内,底层存储会不会被搬动/失效?谁有能力刷新它、在什么时机?「入口锁定、中途不能自刷新」的载体(wazero local)碰到任何「跨层调用后恢复」的点,都是潜在失效点,必须由被调侧返回时回传刷新后的值。⚠️ **解释器恰好因「每次访问经 `th.slot(i)` 现算地址」碰巧免疫,反而掩盖危险——照搬解释器「能跑」不等于加速层「能跑」**(见下「如何用」)。这是 `feedback_arena_view_aliasing`「形态 Y 别名」雷区的 gibbous 帧对偶。

## 3. 成本归类:架构成本 vs 实现浪费——援引前提判否优化前先分类

[[design-premises]] 前提一明写「per-item 跨界被边界成本吃光收益」是设计预期。最危险的反应是直接援引前提把一个性能 issue 判为「已知限制、无需修」。**前提能判否一个优化提案,但不能拿来掩护实现浪费。** 先 profile 定位成本来源,再把成本拆成两类:

- **架构成本**:NaN-boxing/arena/边界拷贝纪律带来的、前提二四项税决定的固定开销——这是设计选择,不该为它违背前提去优化;
- **实现浪费**:与架构无关的纯实现冗余——无论形态是否被前提判为非主推,都该消除。

**实例(issue8)**:`Call` 的 72 B / 2 allocs 表面像「边界成本=架构给定」,实为返回值**双拷贝**(VM 栈→inner slice→public slice,与 nret/脚本复杂度无关)的可消除冗余;`CallInto` 直切活动区零拷贝即消除,不触碰任何前提。

**判据**:收到「在 X 形态下比对标对象慢」时,先问「这笔成本是架构选择的必然,还是可消除的实现冗余?」。只有架构成本才适用「这是已知设计权衡」的回应——尤其当该形态是**首个目标宿主的真实热路径**且对标场景是已承诺的 drop-in 卖点时。

## 4. GC 根可达性——复用栈/共享 arena 返回的值,根还在不在

把内部缓冲区切片直接返回省分配、或让加速层持有指向共享 arena 的值时,**根可达性不能靠推理下结论,必须 stress 实测**。本码库的物理事实:值切片指向复用栈/arena,持有者复位后,只要存在一条常驻根链(如 `Call` 返回后 `runningThread` 复位 nil 但 `mainTh` 仍是同级常驻根),槽位值在 GC 下仍可达。

**实例(issue8 教训 2)**:`CallInto` 零分配直切 `th.stack[:nret]`,用 `SetGCStressMode(true)`(每分配点触发 GC)+ 复用 dst 循环 + string 返回值(经 arena)500 轮读出仍正确 = 无 UAF;配套覆写契约(返回值下次进 VM 前被覆写,godoc ⚠️ 标注)。

**判据**:任何「返回内部缓冲区切片以省分配」或「加速层持有共享 arena 值」的优化,GC stress 实测 + 覆写契约测试是上线前置,不是可选。与 `feedback_arena_view_aliasing` 同物理基础。

## 5. 时间维度——设计稿/task/stub 注释承诺/外部依赖现状的前序快照在事实变更后失效

前 §1-§4 是**空间不变式**(边界成本/段重定位/成本归类/根可达性)——同一时间点对码库 physics 的判断。**时间维度**是不同时间点的快照失效:设计稿/task 描述/stub 注释承诺/外部依赖现状,在写下当时为真,但**后续事实变更(前序里程碑落地 / 闸门翻面 / 外部依赖升级)使快照过期**,照搬过期快照导致死优化或潜伏 bug。

**四个独立形态**:

| 形态 | 实例 | 快照内容 | 失效触发 |
|---|---|---|---|
| **难点过期** | PW7 CLOSURE/CLOSE 设计稿 §3.7 标核心难点是「open upvalue 存储协议」,实际 VS0-c 已解 | 设计稿对里程碑难点的判断 | 前序里程碑(VS0-c)追溯性溶解后续难点 |
| **task 描述失实** | VS0-e task #6 三项里两项已被 VS0-c + PW10 R2 隐性收口 | task 描述对工作量/范围的快照 | 延后 ≥6 个月的 task,中间隐性交付 |
| **外部依赖现状过期** | P4 doc-review:wazero internal/engine/compiler 已切 wazevo / darwin/arm64 MAP_JIT 实装不存在 / Go 1.22 已过保 | 设计文档对外部依赖现状的引用 | wazero / Go / 平台 API 升级 |
| **sentinel 注释承诺与闸门状态解耦** | `arch_arm64.go::archSseOpForArith` stub 注释「当前 archSupportsSpec 返 false,本函数不会被调用——sentinel 返 (0, false) 保底」,但闸门翻 true 时本函数被真调,stub 静默返 (0, false) 让所有 PJ2 spec 测试 0 命中 | stub 默契承诺(「信本函数不会被调用」) | 闸门/配置/上下文状态翻面 |

**sentinel 注释承诺形态特征**:「**当前 X 为 Y,所以本函数 Z**」——X 是闸门/配置/上下文状态,Y 是当前值,Z 是基于 Y 的安全行为(stub / sentinel / no-op)。注释当时为真,X 翻面时若未同步审计本函数,Z 行为变成**静默 bug**——没有 panic、没有 error、没有日志,只是性能数据上「投机路径 0 命中」或正确性上「fallback 永远走到」。

**判据**:
- **写 stub / sentinel 时,若注释含「当前 X 为 Y,所以本函数 Z」,在 X 翻面的同一 commit 必须同步审计本函数**——`grep -rn "当前.*返 false\|当前.*不会被调用\|sentinel.*保底" <pkg>` 是闸门翻 true 同 commit 的标准动作;
- **stub 默认应 panic 而非保底**——「保底」是默契承诺(信本函数不会被调用所以静默),panic 是显式契约(若被调用立刻爆);panic 让闸门翻 true 时此类 bug 立刻可见,sentinel 让其潜伏;
- **「sentinel 静默」+「测试断言命中率」是配对纪律**——若性能敏感不能 panic 必须用 sentinel,配套测试必须断言「该路径被走到」(本会话 PJ2 spec 测试断言命中率 > 0 抓到了 bug),否则就是结构盲区;
- **接延后 ≥6 个月的 task 前必先重核每一项现状**(task 描述可能失实);**大文档发布/审查前对外部依赖现状做事实层 checklist**(外部依赖现状过期);**开始任何标注「难点」的里程碑前先核实难点是否仍在**(前序里程碑可能已解)。

这四个形态共享同一物理基础:**前序事实变更后,快照不会自更新,须显式审计**。§1-§4「四空间内部维度」+ §5「时间维度」+ §6「空间维度」(见下)构成六维判据。

## 6. 空间维度——跨 OS / shell / runtime 版本物理环境差异

前 §1-§4 是**码库内部 physics**(边界成本/段重定位/成本归类/根可达性),§5 是**时间维度的事实漂移**(前序快照失效)。**空间维度**是同一时刻、不同部署/开发环境之间的**物理差异**——同一份脚本/工具链/依赖,在 OS A / shell 版本 X / runtime W 下跑得通,换到 OS B / shell 版本 Y / runtime Z 下静默挂或行为不同。设计稿写「Linux/bash 4+」假设,跑到 macOS/bash 3.2 上**没有 panic、没有 error**,只是 `mapfile` not found / `${arr[@]}` 在 `set -u` 下 unbound exit / `declare -A` 报错——dev 环境与 CI runner 的「单平台 / 单维度」掩护一旦撤掉(矩阵 CI 扩平台、新 OS 接入),笛卡尔积上的 latent 问题一次性翻面。

**触发场景**:写 shell 脚本要跨 OS 跑(`.githooks/pre-commit`、`scripts/*.sh`)、CI 矩阵加 `ubuntu-24.04-arm` / `macos-latest` 维度、加 `actions/cache` 缓特殊文件类型(symlink / 嵌套 dir)、brew/apt 包政策更迭(homebrew 2024 后撤 `lua@5.1`)、cross-arch / cross-OS 真物理 runner 首次接入。

### 6.1 典型空间维度差异(三类物理环境分类例)

| 类别 | 维度 | 差异示例 |
|---|---|---|
| **OS** | linux / darwin / windows | 默认 `/bin/bash` 版本(linux 4+/darwin 3.2/windows 不带)、`tar` 行为(symlink 解引用)、coreutils GNU vs BSD(`sed -i ''` darwin 需空串、`stat` 字段不同) |
| **Shell 版本** | bash 3.2 / 4+ / 5+ / zsh | 数组语法、关联数组、`mapfile`、`${var,,}` 大小写转换 |
| **Runtime / 工具链** | Go 1.22 / 1.23+(`min`/`max` 内建)、wazero compiler→wazevo、libc glibc/musl、Lua 5.1.5 / luajit / lua@5.4 | 内建函数集、JIT 后端、动态链接行为、homebrew 包政策(`brew install lua@5.1` macOS 2024 后已撤) |
| **CI runner** | ubuntu-latest / ubuntu-24.04-arm / macos-latest(=M1+) | runner image 默认 PATH、可用 brew 包、`actions/cache` 对 symlink 的 tar 语义 |

### 6.2 bash 3.2 vs 4+ 兼容性 cheatsheet(macOS 默认 `/bin/bash` 至今 3.2.57,GPLv3 政策长期约束)

| 形态 | bash 3.2 行为 | bash 4+ 行为 | 跨版本兼容写法 |
|---|---|---|---|
| **空数组展开 + `set -u`** | `${arr[@]}` 在 `arr` 为空时视作 unbound → 触发 `set -u` exit | `${arr[@]}` 空数组安全展开为零参数 | `${arr[@]+"${arr[@]}"}`(参数扩展默认值语法,空数组时整个被替换为空) |
| **`mapfile`(数组从命令读)** | 不支持(bash 4.0+ 内建) | `mapfile -t arr < <(cmd)` 一行从 stdin 装数组 | `while IFS= read -r line; do arr+=("$line"); done < <(cmd)` 显式循环 push |
| **`declare -A`(关联数组)** | 不支持(bash 4.0+ 引入) | `declare -A m; m[key]=val` | `pairs+=("$key"$'\t'"$val")` 把 key/val 用 `\t` 拼成元组数组,然后 `printf '%s\n' "${pairs[@]}" \| sort -u \| while IFS=$'\t' read -r key val; do ...` 分组循环 |

**实例(PR #28 三连)**:本仓库 `.githooks/pre-commit` + `scripts/go-fuzz.sh` 写 Linux 默认 bash 4+ 风格,接 macOS dev 环境 + macos-latest CI runner 时三处独立踩雷,同 commit `61dadde` 改三处跨版本兼容惯用法。**`mapfile` not found / `declare -A` 报错** 是显式失败易抓;**`${arr[@]}` + `set -u`** 是隐式失败——空数组场景未到时静默,触发时直接 exit,**默契承诺式 bug**(与 §5 sentinel 注释承诺同源:都是「当前 X 为 Y 所以本路径 Z」家族,X=bash 版本 / shell 平台)。

### 6.3 actions/cache 对 symlink 的物理语义

`actions/cache` 底层用 `tar` 打包路径,**默认 `tar` 把 symlink 当 symlink 存(只存指向字符串),不递归 symlink 目标**——cache hit restore 后 symlink 还在,目标不在(目标在另一个未缓的路径) ⟹ symlink broken。

**实例(PR #28 paper cut #2)**:macOS lua5.1 源码编译装到 `/usr/local/lib/lua-5.1.5/lua` + 在 `/usr/local/bin/lua5.1` 创 symlink,缓 `/usr/local/bin/lua5.1` 单点 → restore 后 symlink 指向已不存在的真二进制。**正确做法**:缓**编译产物源目录**(`lua-5.1.5/`,已 `make` 出 `src/lua`/`src/luac`),restore 后必跑 `make install` 重新装+创 symlink(~1s restore + ~0.1s install,比从头编译 ~10s 快 10 倍且 symlink 一定正确)。

**判据**:任何 cache 路径在 cache save 前 `ls -la` 看是否 symlink,是则切「缓源目录 + restore 后重 install」模式。这是 POSIX `tar` 默认语义不是 `actions/cache` bug,但效果上等价于 cache 半残。

### 6.4 判据与 grep checklist

- **写 bash 脚本要在 macOS dev 环境 / macos-latest CI 跑前**:`grep -nE 'mapfile|declare -A|\$\{[a-zA-Z_]+\[@\]\}' scripts/ .githooks/` 是提交前默认动作;`${arr[@]}` 命中且脚本含 `set -u` 时,改 `${arr[@]+"${arr[@]}"}`;`mapfile` / `declare -A` 命中时按 §6.2 改兼容写法;
- **CI 矩阵加 `ubuntu-24.04-arm` / `macos-latest` 维度前**:列「新维度上有哪些与原维度不同的物理环境差异」清单——OS / shell 默认版本 / 包管理器 / runner image 默认 PATH,**逐项预审**;
- **加 `actions/cache` 路径前**:cache save 前 `ls -la` 看路径是否 symlink,是则切「缓源目录」模式;
- **接 cross-arch / cross-OS 真物理 runner 首次接入时**:[[prove-the-path-under-test]] §5「CI runner 形态盲配套」与本节合用——既要证「这台 runner 真在跑真物理 execute」(prove-the-path),也要审「这台 runner 物理环境与本机有什么差异」(本节)。

§6 与 §5 的对偶:**§5 时间维度**(前序事实变更使快照失效,纵向)+ **§6 空间维度**(并行部署环境物理差异,横向)= 「设计稿假设的物理前提在实际世界变化」家族双面;两者共享「快照/默认假设不会自验证,须显式审计」的元纪律。

## 如何用

- **设计稿记号是语义契约,不是物理承诺**:`(call $x)`、`$base`、「成本=架构给定」都只声明「这里要什么语义」,从不声明本码库 physics(边界成本/段重定位/根可达性)。实现前逐条过 §1-§4。
- **快照不会自更新,事实变更须显式审计**(§5):设计稿/task 描述/stub 注释承诺/外部依赖现状在写下当时为真,前序事实变更后必须显式重核——尤其闸门翻 true 的同一 commit、延后 ≥6 个月的 task 接续前、大文档发布前。
- **跨部署环境差异不会自暴露,矩阵扩面前须预审**(§6):同一份脚本/工具链/依赖在不同 OS / shell 版本 / runtime / runner 之间静默挂。CI 矩阵扩平台、加 `actions/cache` 缓特殊文件、cross-arch 真物理 runner 首次接入前,逐维度过 §6.1-§6.3。
- **解释器「能跑」≠ 加速层「能跑」**:二者刷新地址/管理生命周期的能力不同——解释器每访问经 `th.slot()` 现算,gibbous 的 `$base` 入口锁定中途无法自刷新。设计稿往往因解释器碰巧免疫而对危险盲视,加速层照搬就触雷。先问「谁有能力刷新、在什么时机」。
- **逐条对照,而非整体信任**:边界成本预算(§1)、段重定位(§2)、成本归类(§3)、根可达性(§4)、时间维度(§5)、空间维度(§6)是六个独立维度;一条主张可能同时踩多个。

## 关联

[[issue8-boundary-cost-round]](家族奠基:实现浪费 vs 架构成本 + 零拷贝根可达性)· [[p3-pw5-table-ic-round]](边界成本维度:`$helper` 须按 ~143ns 预算重判)· [[p3-pw6-crosslayer-call-round]](内存物理维度:`$base` 须按 arena 段重定位重核)· [[p3-pw7-pw4b-closure-tforloop-round]](§5 时间维度:难点过期)· [[vs0e-varargs-stack-underflow-round]](§5 时间维度:调研先于实现)· [[p4-doc-review-round]](§5 时间维度:外部依赖现状过期)· [[pr27-f3-3b-darwin-arm64-execute-roundup]](§5 时间维度:sentinel 注释承诺与闸门状态解耦)· [[pr28-f3-3c-tri-platform-matrix-ci]](§6 空间维度:bash 3.2 vs 4+ / actions/cache symlink / homebrew 包政策)· `feedback_arena_view_aliasing`(arena=linear memory 段可重定位 / 偏移现算寻址,§2/§4 物理基础)· [[design-premises]](前提一边界成本论证 / 前提二四项税,§1/§3 成本根据)· `docs/design/p3-wasm-tier/02-translation.md` §3.4 · `04-trampoline.md` §2-§4
