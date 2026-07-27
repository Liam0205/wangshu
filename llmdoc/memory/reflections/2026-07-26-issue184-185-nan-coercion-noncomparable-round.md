---
name: 2026-07-26-issue184-185-nan-coercion-noncomparable-round
description: >
  issue #184 / #185：2026-07-25 nightly-diff-fuzz p1 腿撞出两个 `FuzzOracleDiff`
  crasher（`print(string.format("NA%E",-(0%0)))` 与 `print(tostring(0%0))`），都落在
  PR #181（issue #173）刚机制化的 NaN 符号已知差异区域。先用探针把整个「NaN 数值 →
  文本」的出口面测了一遍，测出两件事：一是 7 种写法（`tostring`、`..`、`%s`、`%q`、
  `string.rep`、`table.concat`）完全没有 span 证据，因为 `preludeCapture` 只在
  print/io.write 直接见到数值 NaN 时记 span；二是 NaN 符号差异会泄漏成纯数值差异
  （`string.len(0%0)` oracle 4 / wangshu 3，`string.byte` 45 vs 110，
  `string.len(0%0)*100` 400 vs 300），`print(400)` vs `print(300)` 里没有任何 NaN
  token，span-anchored 拼写分类在结构上覆盖不到。这说明 #173「已知平台差异」的定性
  适用边界比当时认定的窄：对走各引擎 printf 的浮点转换成立，对文本 coercion 不成立。
  用户在三个方案里选了 B —— 把 NaN 文本 coercion 整类判为不可比，两侧对称 skip，产品
  代码零改动。实现是新增 `preludeNaNCoercion` 层，全局函数出口（`tostring`、
  `string.len/sub/byte/upper/lower/reverse/rep`、`table.concat`、`%s`/`%q`）直接抬
  `LimitSentinel`；`..` 操作符在 Lua 5.1 层面无法拦截（读 vendored `lvm.c` 的
  `luaV_concat` 核实它对 number 直接走 tostring 宏、不触发元方法），改在输出端用
  `__emit_nan_gate` 对无 span provenance 的字符串做大小写敏感 plain 查找兜底。
  过程中自己踩了一次假绿：门层用 Lua pattern 匹配 `%s`/`%q`，但门跑在
  `preludeGuards` 之后，`string.find` 已被 `__patcheck` 包装、量词超 2 个即报
  `pattern too branchy`，两侧对称报错 → 输出都成空串 → `CompareOutput` 判 equal，
  检查静默失效。改成手写逐字节扫描 + 把原始 `string.find` 经 harness 私有全局
  `__oracle_sfindraw` 传给门层。
  这一轮写完之后又做了一次独立本地审计：固定 SHA 的隔离快照 clone、`bwrap
  --unshare-net` 断网边界实测验证、启动无继承上下文的盲审 reviewer（只给仓库规则、
  任务目标、固定范围和它自己在快照里跑出来的原始证据，不给父对话、预期结论和可疑
  点）。盲审结论 REQUEST_CHANGES：3 blocking / 3 important / 2 minor，逐条用双侧
  探针核实后 **8 条全部成立**。最严重的一条是判据不对称：`__emit_nan_gate` 的
  needle 写成了本引擎自己的 NaN 拼写 `__tostring(0/0)`，于是 PUC 侧找 `-nan`
  （4 字节）、wangshu 侧找 `nan`（3 字节），同一个脚本两侧判据不同 ——
  `print("banana")` 只在 wangshu 侧抬 sentinel，而 `FuzzOracleDiff` 的逻辑是「任一侧
  `VerdictLimit` 就 skip」，于是这个输入连同同一次运行里所有真实差异一起被静默丢掉
  （实证 `print("banana") print(string.len(""..(0/0))*100)`，后半句 400-vs-300 的
  差异当时仍是活的）。修法是把 needle 换成引擎无关的固定小写字面量 `"nan"`。第二条
  blocking 是 `TestExec_NaNGateSymmetry` 在结构上无法测 symmetry（`internal/oracle`
  只跑 cgo PUC shim，从不实例化 wangshu），已改名收窄并把真正的两侧断言移到根包新
  文件 `oracle_nan_gate_test.go`；第三条是漏了 `string.find/match/gmatch/gsub` 这组
  同样走 `luaL_checklstring` 的出口。修完 8 条后仍留一条已知边界：`..` 的结果直接
  交给 `#`（OP_LEN）或 `==` 时，符号差异被压成纯数字、输出里不留 NaN 字节，prelude
  没有任何观察点；这是 Lua 5.1 语义决定的硬边界，用户决定接受残余并明确写进文档，
  用测试固化让它公开可见、不会悄悄扩大。
  第一轮修完之后又做了一次增量盲审（新的隔离快照 clone、`bwrap --unshare-net` 断网
  实测验证、新的无继承上下文 reviewer，范围只看第一轮那三个修复 commit
  `4d45dad..d0eaba1`），结论 REQUEST_CHANGES：1 blocking / 2 important / 3 minor，
  逐条核实后**又是全部成立**。blocking 是我**在修第一轮问题时引入了新的同类缺陷**：
  为覆盖大写跨 concat straddle，我加了 `__fmt_nan_rendered` 标记 + 大小写不敏感扫描，
  当时的推理是「标记由两侧相同的数据（fmt 字符串 + 参数类型）算出，所以对称」；
  这个推理漏了一步 —— 标记对称，但扫描**读取的对象**不对称，读的是运行时字符串内容，
  而内容两侧不同（`%E` 在 oracle 侧得 `NAN`、wangshu 侧得 `-NAN`）。审计构造出偏移
  差 1 就翻转方向的双向实证（`sub(1,4)` 是 oracle Limit / wangshu OK，`sub(2,5)`
  反过来），并在 base commit 上跑同样两条得到 `different` 而非 `skip-asymmetric`，
  证明这是本轮引入的回归。修法是删掉标记和扫描，大写跨 concat straddle 改报**对称的
  差异**并归入已知边界 —— 宁可报一个会亮红灯、人能看到的对称差异，也不要静默吞掉同
  一次运行真实差异的单侧 skip。两条 important 是 `__gate1` 判据写成「string 参数整串
  恰为某个 NaN 拼写」而带任何前后缀就绕过（实测 8 种写法全未拦，使新写的注释与
  `TestFuzzOracleDiff_NaNGateCoverage` 声称的「覆盖每一种经库函数的写法」口径不成立，
  且三份设计文档照抄了这个口径），以及 `TestExec_NaNSpansKnownLimit` 被我改成只断言
  `VerdictLimit`、于是不再观察它名字所指的 FIFO provenance span 错位。已知边界的
  准确表述也随之修正：不是原来写的「`..` 链到 `#` 或 `==`」那么窄，而是**coerced NaN
  文本被消费时没有经过任何被拦函数**（含 `#`、`==`、以及切片后只留大写字节）。
  第三轮又做了一次全范围盲审（新快照 clone、`bwrap --unshare-net` 断网实测验证、新的
  无继承上下文 reviewer，范围回到完整的 `c982610..92241f4`），结论 REQUEST_CHANGES：
  1 blocking / 2 important / 3 minor，核实后仍是全部成立。blocking 是**同一类错误的
  第三个实例**：`__nan_provenance` 用 coerced 字符串本身作 key，而那个字符串两侧不同
  （PUC `-nan` / wangshu `nan`），于是同一个 `string.format` 调用在两侧被记到不同的
  key 下；`__emit_nan_gate` 的豁免判断要查这张表，所以 format 结果算出来但没被 print
  消费时，provenance 一直挂着、且挂的 key 每侧不同 ——
  `local _ = string.format("%e", 0/0) print("nan")` 是 oracle Limit / wangshu OK，
  换成 `print("-nan")` 就反过来，并且会吞掉同一次运行里的真差异。触发面不窄：format
  结果被丢弃、进 pcall、进表构造器、做条件判断都算；我第二轮写的
  `TestFuzzOracleDiff_NaNGateSymmetry` 漏掉它，是因为那 30 条用例里每个 format 结果
  都被直接 print 消费了。修法是 `__prov_key(s)` 剥掉一个前导符号后作 key，让两侧落进
  同一个桶。两条 important：`table.concat` 预扫跑在真 `tconcat` 校验参数之前，非数值
  `i`/`j` 先触发预扫里的数值 `for`，把 `tconcat` 自己的 `bad argument #4` 盖掉，而那
  条消息两侧本来有真差异，于是真差异被掩盖成 equal（`j` 给 `1e15` 还会让预扫真循环、
  两侧各烧数百毫秒撞预算判 skip，base 上 1ms 就报出差异）—— **我加的检查把一条既有的
  真差异藏了起来**，这是检查本身有副作用的失效模式，与判据不对称是两回事；以及
  `nan_straddle` 的注释把覆盖面说得比实际宽（只处理单个 fmt 字符串内的前缀方向）。
  第三轮修完之后我没有等下一轮盲审，而是按教训 2 自己说的做了一次机械枚举：`grep` 出 gate
  路径上每一处读取点（`__sfindraw` / `__nan_provenance` / `__prov_key` / `__isnan` /
  `__nan_needle` / `#s`），逐个问「这个值两侧相同吗」，再把枚举变成常驻回归测试
  `TestFuzzOracleDiff_NaNGateNoAsymmetricSkip`（根包，29 种写法，覆盖 provenance 登记
  未消费、乱序消费、同值产生两次只消费一次、被 concat、进 pcall / 表构造器 / 条件判断、
  与两种拼写的脚本字面量碰撞），断言「任何输入不得只在一侧 skip」；反向验证把 key 改
  回按值索引，测试报 `7/29 shapes decide asymmetrically`，证明它真的起作用。
  第四轮又做了一次全范围盲审（新快照、新的无继承上下文 reviewer，范围 `c982610..a7197a8`），
  结论 2 blocking / 2 important / 2 minor，核实后**仍是全部成立**。第一条 blocking 是
  我上一轮的 `__prov_key` 只是**部分修复**：剥符号只看第 1 字节，而 PUC 的 `-nan` 只有在
  nan token 紧贴串首时符号才落在第 1 字节，fmt 一旦带字面前缀或宽度 padding，符号就落到
  串中间，两侧 bucket key 又不同（实测 `"x%e"` / `"z%e"` / `"%5e"` / `"%e %e"` 四种单侧
  skip）。第二条 blocking 是 span 长度按记录方引擎的 token 记（PUC 4 字节含符号），被 3
  字节同值字面量提前消费后 span 末端越过 body 长度，`DecodeOutput` 判 `ok=false`，PUC 侧
  变成 `VerdictLimit: invalid readout` 而 wangshu 侧正常。两条 important 里更重要的一条是：
  我上一轮那 29 条枚举测试里 `string.format` 的 fmt **全是裸 `%e`/`%E`/`%f`/`%g`** ——
  恰好是 `__prov_key` 唯一能正确归一化的写法，加一个字面前缀立刻报不对称；我以为自己做了
  「机械枚举」，其实**枚举的维度选错了**（枚举了 provenance 的消费时序，没枚举 fmt 的写法）。
  第四次同类缺陷之后我不再补 key，改去问「这个机制能不能做对」，一步就证明**不能**：要保住
  #173 的 span 分类，输出端检查必须豁免浮点转换结果，而唯一可用的豁免判据是「这个字符串有
  没有 provenance」——Lua 5.1 没有 string identity primitive，只能按字符串**值**查表，而那个
  值恰恰就是两侧的差异。四轮的四个缺陷都是同一个不可对称的 key 的变体。用户先选「改产品侧
  让 wangshu 输出 `-nan`」，我改了 `formatLuaNumber` 但输出没变，查出根因在 `internal/value/value.go`
  的 `NumberValue` 把所有 NaN 规范化成单一 `canonNaN`，符号位在值层就丢了；用户再选「真改
  NaN-boxing 保留符号位」，spike 实测结论是硬件层面不可行 —— x86 上每一个产生 NaN 的浮点
  运算都返回 `0xFFF8_0000_0000_0000`，与 `TagNil` 逐位相同，负 quiet NaN 在这个布局里没有
  容身之处；负 signaling NaN 虽落在 number 空间内，但硬件从不产生它，要用就得在 P1 解释器、
  P3 wasm、P4 amd64 与 arm64 四条代码生成路径上给每个浮点运算结果做重映射，漏一处 NaN 就
  被读成 `nil`，且 `float32` 窄化会把它变回撞 tag 区的编码；顺带实测 PUC 本身也不是固定
  `-nan`，它跟随符号位。两次实测否决之后用户决定**删掉整个输出端检查**（commit `2613871`），
  保留入口拦截 + #173 通道 + #184 straddle 规则，两个 issue 仍然解决；`..` 的 coercion 直送
  输出改为报**对称差异**（nightly 能看到、按已知边界处理），换回来的收益是之前被误跳的普通
  文本恢复可比（`print("banana")` / `print("finance")` / `print("nan")` / `print("-nan")`）。
  第五轮又做了一次全范围盲审（1 blocking / 2 important / 1 minor，核实后**全部成立**）。
  blocking 是这一类缺陷的**第五个实例，而且又是我修上一条时引入的**：第三轮为了消掉
  `table.concat` 预扫的副作用，我把预扫范围钳到 `#t`，而带空洞的表 `#t` 在 Lua 5.1 里
  未规定、两侧引擎取值不同，于是跳过决策变成引擎相关 ——
  `local t={1} t[4]=4 t[2]=0/0 print(table.concat(t, ","))` 在 oracle 侧 Limit
  （`#t`=4，NaN 在扫描范围内）、wangshu 侧 OK（`#t`=2，NaN 在范围外），实测确认单侧、
  且确认会吞掉同一次运行里的真差异。这一次终于**按教训 1 做了**：不再改钳范围的算法，
  而是问「这个预扫的前提对不对」—— 预扫要复制 `tconcat` 自己的遍历，就必须猜它的边界，
  而默认边界 `#t` 无法两侧一致地猜。改成检查**结果字符串**：`tconcat` 实际读了什么都在
  返回值里，完全不需要边界，判据退化成同一个固定小写 needle。教训 1 第一次真的被用上，
  而且方案反而变简单了。两条 important：`string.gsub` 的 **replacement** 参数从未被检查
  （`string.gsub("abc","b",0/0)` 把 coerced 文本直接放进结果）；`%s`/`%q` 槽位检查用的是
  `__isnan`，只认 NaN 数字，看不见 `..` 已经转好的字符串 —— 两处都改走 `__gate1`。枚举
  测试也补强了：审计指出那 29 条里 `string.format` 的 fmt 全是裸 verb、表全是无空洞的，
  恰好是当时那个缺陷能正确处理的写法；补了 fmt 写法（字面前缀 / 宽度 / padding / 多转换）、
  表写法（空洞 / 间隙 / 稀疏）、参数位置（replacement / `%s`·`%q` 槽位）三个维度，现在
  47 条，反向验证把 `#t` 预扫改回去报 `3/47` 不对称，而旧的 29 条一条都报不出来。
  第六轮再做一次全范围盲审（1 blocking / 2 important / 3 minor，核实后**仍是全部成立**），
  blocking 是**一个全新的维度：运行成本**。`__sq_arg_slots` 逐字节扫格式串，80 KiB 的
  fmt、既不含 NaN 也不含任何转换、调用 60 次：PUC 用 392ms 判 OK，wangshu 跑 1m22s 之后
  `instruction budget exceeded` 判 `VerdictLimit`，输入被整条丢掉；base 上两侧都 OK
  （131ms / 216ms）。前五个实例都是「判据读的**值**两侧不同」，这个是「判据**自身的开销**
  两侧不同」—— harness 里每个检查在 wangshu 侧是解释执行的 Lua、在 PUC 侧是 C，所以开销
  随输入规模增长的检查会让一侧撞预算，后果一样是单侧 skip，只是经由时钟。修法两步：扫描
  用 plain find 跳到下一个 `%`（复杂度 O(fmt 长度) → O(转换个数)）；扫描（含 straddle
  walk）只在「确实可能有参数被拦」时启动（参数含 NaN 数字，或含已带符号的 coerced 字符串，
  两个判据都是每参数 O(1) 且由参数类型推导，所以两侧一致），实测 38s → 264ms，与 base 的
  216ms 同一档。成本测试自己也踩了一次坑：新增的
  `TestFuzzOracleDiff_NaNGateNoCostAsymmetry` 第一版只断言「两侧 verdict 一致」，把 bug
  改回去它**照样通过** —— wangshu 慢了 74 倍但仍在预算内跑完；必须断言的是**成本比值**，
  因为那才是「输入再大一点就变单侧 skip」的量，每个写法与自己的短输入版本比，让共享机器
  负载影响比值两端而不影响结论，反向验证报 645 倍 / 上限 120 倍并失败。两条 important：
  `TestExec_NaNGateEngineIndependentNeedle` 之前**没有它声称的判别力**（把 `__gate1` 的
  固定小写 needle 换成注释里明令禁止的 `__tostring0(0/0)`，测试照样通过），补
  `print(string.len("banana"))` 并验证 needle 退化时真的会失败；以及 coerced 文本进到
  **pattern** 参数仍会在两侧不同的点触发 `__patcheck` 的量词上限，且 `__patcheck` 的错误
  文本不在 `SkipClassError` 里，所以报成 class 分歧。minor：`fmt_was_nan` 分支不可达已删，
  边界清单补上「函数/表形式的 gsub 替换」与「大写 coerced 文本经被拦函数」。
  第 7 到第 26 轮又做了二十次独立盲审（每次都是新的隔离快照 clone、`bwrap --unshare-net`
  断网实测验证、新的无继承上下文 reviewer），分支现在 43 个 commit。这二十轮的结论可以压成
  一句话：**26 轮里有 13 轮抓到的缺陷是我修上一轮时引入的**，而缺陷集中在两处，每处都在
  同一个抽象上反复失败。第一处是 span recorder 吸收符号列与 padding 的逻辑，一共改了
  **六个版本**，每一版都修好了上一版的问题、又引入一个新的：① 逐字节 walk 且无上限 ——
  wangshu 每字节付一次解释执行、PUC 是 C 循环（1m45s vs 384ms），单侧 skip；② 用 offset
  给 walk 加上限（从 tokStart 数）—— 两侧输出长度差一个符号字节，于是所有绝对 offset 差 1，
  停止位置错开一字节，span 锚点不同，一个已知符号差异变成硬失败；③ 改从 token 自身位置数
  —— 同一个问题，那个 offset 本身也是偏移过的；④ 一次无锚定 match
  `" *[%-%+]*[Nn][Aa][Nn] *"` —— 前导量词在每个起始位置重试，代价是运行长度的平方，约
  1024 字节就撞 wangshu 的 matcher 步数预算（`maxMatchSteps`），而 PUC 的 C matcher 没有这个
  预算，脚本用 pcall 包住时甚至是一次假分歧；⑤ 换成 `$` 锚定的 `" *[%-%+]*$"` —— 平方问题
  只是换了位置；⑥ 最终收敛的写法不含任何量词：用字符类查找定位「不属于 run 的那个字节」，
  再把 gap 反转过来按前缀一次匹配 —— 无重试、无迭代、无 offset 运算。
  第二处是「限制扫描规模」，失败了 **五次**，每次都是同一个错误 —— 比较了一个两侧不相等的
  长度：输出长度、输入长度、分桶长度、放宽容差的长度、带内容例外的输入长度。最终结论是
  **根本不比较长度**：把扫描本身做成不需要上限的（plain find 是逐字节搜索、没有 matcher
  步数预算；需要 pattern 的地方只在固定长度的切片上跑），八处扫描点全部按这个方式改写。
  这二十轮还找出三个此前没想到的不对称维度，连同第六轮的运行成本一共四维，每一维都配了
  专门的守护测试（gate 测试现在 9 个）：**运行成本**（harness 的检查在 wangshu 侧是解释执行
  的 Lua、在 PUC 侧是 C，开销随输入规模增长的检查会让一侧撞 step budget）；**分配量**（PUC
  的 GC 在 alloc 帽内回收临时串，wangshu 的 arena 回收不了，同样的 walk 会让一侧撞 arena
  上限）；**matcher 步数预算**（只有 wangshu 给 pattern 重试计步）；**长度阈值**（任何长度都
  可能两侧不等，因为 `..` 强制转换出来的 NaN 文本带一个符号字节，而 `#` 与 `==` 能把那个
  字节搬进一个数字）。另外几件值得单独记的：第 5 轮那个 `table.concat` 预扫改用 `#t` 作
  上界之后，带空洞的表 `#t` 在 5.1 未规定、两侧不同，最终改成无条件先扫元素（自 1 起、忽略
  脚本的 `i`/`j`、有循环次数上限）再检查返回值，把判定入口从「tconcat 是否失败」这个引擎
  相关的条件上摘下来；第 17 轮抓到 `__concat_scan_cap` 在上限处抬 sentinel，导致**所有**超过
  4096 元素的表整类不可比，concat 的边界处理与错误文本比对全丢掉，改成上限只限制循环次数、
  不改变判定；第 20 与第 21 轮是成本测试自己「看不到它命名保护的东西」—— 先是断言两侧
  verdict 相同，而慢 74 倍仍在预算内所以照样通过，后来输入上限又让 12 个大变体里的 9 个在
  扫描启动前就短路。产品侧这一轮出现了一处改动（不再是零改动）：`string.find` 的 plain 路径
  改用 `bytes.Index`，去掉每次调用复制剩余 subject 的开销（30 倍长的输入曾表现为 205 倍的
  成本增长）。另有两次产品侧尝试被实测否决并已 revert：改 `formatLuaNumber` 无效
  （`internal/value/value.go` 的 `NumberValue` 把所有 NaN 规范化成单一 `canonNaN`，符号位在
  值层就丢了），改 NaN-boxing 保留符号位不可行（x86 上每个 NaN 运算都产出
  `0xFFF8_0000_0000_0000`，与 `TagNil` 逐位相同；负 signaling NaN 要在 P1/P3/P4 四条代码生成
  路径上给每个浮点结果做重映射，漏一处那个 NaN 就被读成 nil）。
  **本文记录的是被放弃的路线（2026-07-26 转向，共 31 轮盲审，正文详录到第 26 轮）：这个
  机制装错了位置，NaN 符号差异最终改在 oracle 的渲染处消除（oracle 是我们自己 vendor 并用
  cgo 编译的，31 轮里从未查证过这件事），本文的 span 机制与 `preludeNaNCoercion` 整层全部
  删除。最终方向见 `2026-07-26-oracle-nan-render-redesign.md`；本文保留是因为它是「下游
  识别为何不可能收敛」的完整证据。**
metadata:
  type: reflection
  date: 2026-07-26
---

# issue #184 / #185：把 NaN 文本 coercion 判为不可比（2026-07-26）

> **这份文档记录的是被放弃的路线。最终方向见
> [[2026-07-26-oracle-nan-render-redesign]]。**
>
> 放弃的原因一句话：这个机制装错了位置 —— NaN 符号差异应该在**产生它的地方**（我们自己
> vendor 并用 cgo 编译的 oracle 的渲染处）消除，而不是在下游的 harness 里识别并豁免；
> 下游识别必须依赖判据，而那个符号字节一进字符串就是普通数据，判据的输入迟早会被污染。
> 最终方案在 `internal/oracle/lua515.c` 里覆盖 `lua_number2str` 并对 `lstrlib.c` 局部
> shadow `sprintf`，本文记录的 span 机制与 `preludeNaNCoercion` 整层全部删除，
> `CompareOutput` 回到「归一化地址后逐字节比较」，相对 master 约 −550 行，而覆盖面反而
> **增加**（两个 crasher 与整个「泄漏成数字」的家族从 skip 变成普通的 equal）。
>
> 本文保留，因为它是**这条路为何不可能收敛的完整证据**：31 轮独立盲审、13 轮抓到的缺陷
> 是修上一轮时引入的、同一处吸收逻辑六个版本、「限制扫描规模」失败五次。下面的教训 2
> （判据的每个输入都必须两侧相同，含它花掉的时间 / 分配量 / matcher 步数预算）与教训
> 3/8/9/10/11/12 在「已经决定在比较侧处理」的前提下仍然成立；教训 1 的作用域要按新反思的
> 教训 1 读 —— 它管「一个机制内部该不该继续修」，管不到「这个机制装在哪一层」。

> 范围：分支 `fix/184-185-oracle-diff-crashers`，43 个 commit，26 轮独立盲审。前 21 个
> commit（前 6 轮）逐条列在下面；第 7 到第 26 轮的 22 个 commit 按失败模式归到第 17 节，
> 不逐轮罗列。
>
> - e245c17 `test(fuzz): add nightly NaN-coercion crash corpus (#184, #185)`
>   —— 先入 corpus，让下一个 commit 的效果可单独观察
> - 0122e51 `fix(oracle): treat NaN text coercion as non-comparable (#184, #185)`
> - abdba6d `docs(stdlib): scope the #173 NaN classification to float conversions`
> - c45edca `docs: split the NaN oracle exemption into two classes (#184, #185)`
> - 4d45dad `docs(llmdoc): record the #184/#185 NaN coercion round`
> - 0b6c083 `fix(oracle): make the NaN coercion gate symmetric and complete (#184, #185)`
>   —— 独立审计后的修复，8 条问题全修
> - 8bf5353 `docs: correct the NaN gate description after independent review`
> - d0eaba1 `docs(llmdoc): record the independent audit of the #184/#185 gate`
> - e293811 `fix(oracle): drop the asymmetric uppercase sweep (#184, #185)`
>   —— 第二轮增量盲审后的修复，1 blocking（我第一轮引入的回归）+ 2 important +
>   3 minor 全修
> - d3e8d17 `docs: sync the NaN gate description after the second audit round`
> - 92241f4 `docs(llmdoc): record the second audit round on the #184/#185 gate`
> - 4627b4d `fix(oracle): key NaN provenance on an engine-independent bucket`
>   —— 第三轮全范围盲审后的修复，1 blocking（同一类错误的第三个实例）+ 2 important +
>   3 minor 全修
> - 80025fe `test(oracle): enumerate the asymmetric-skip shapes instead of hand-picking`
>   —— 不等下一轮盲审，把「逐个读取点问两侧是否相同」做成 29 种写法的常驻测试；
>   第四轮证明这份枚举的维度选错了
> - 402aa4a `docs: sync the NaN gate description after the third audit round`
> - a7197a8 `docs(llmdoc): record the third audit round and the executable check`
> - 2613871 `fix(oracle): drop the emit-time NaN check, report '..' coercions instead`
>   —— 第四轮全范围盲审（2 blocking + 2 important + 2 minor 全部成立）之后不再补第五次，
>   删掉整个输出端检查；用户决定
> - a9c79f5 `docs: describe the NaN gate as entry-point only`
> - c048dc3 `docs(llmdoc): record round four and the decision to remove the mechanism`
> - d92c6b9 `fix(oracle): check table.concat's result instead of pre-walking the table`
>   —— 第五轮全范围盲审（1 blocking + 2 important + 1 minor 全部成立）后的修复；blocking
>   是这类缺陷的第五个实例，又是我修上一条时引入的；这一次按教训 1 去问预扫的前提，
>   改成检查结果字符串，判据不再需要边界
> - c650885 `fix(oracle): make the fmt scan cost-symmetric, drop dead branch`
>   —— 第六轮全范围盲审（1 blocking + 2 important + 3 minor 全部成立）后的修复；blocking
>   是全新维度「运行成本两侧不同」，逐字节扫格式串让 wangshu 侧撞预算
> - 55d880c `docs: record the cost dimension and three more known boundaries`
> - ab9869a `docs(llmdoc): record rounds five and six`
> - 第 7 到第 26 轮的 22 个修复 / 测试 commit（`ad32021`..`e5cb293`）按失败模式归到
>   第 17 节。其中吸收逻辑那六版依次是 `e9757c8` → `d6ec704` → `a041c17` →
>   `dafa90f` → `59cbad4` → `2e7dc57`（含 `f8af187` 补后缀方向）；长度阈值那五次是
>   `898e206` → `5c9caf7` → `2c9476a` → `2e7dc57` → `e5cb293`；`c00dcc5` 修
>   `__concat_scan_cap` 在上限处抬 sentinel 使大表整类不可比；`426bf37` 把 concat
>   的判定入口从「tconcat 是否失败」上摘下来；`ad32021` / `a5d4232` / `43bebb8` /
>   `67be883` / `3ee1940` / `d6bad22` 是各处扫描的成本与分配量修复；`6574ea0` /
>   `000738c` 是测试自身的判别力修复。
>
> 改动文件 `internal/oracle/prelude.go` + `internal/oracle/oracle_test.go` +
> 新增根包 `oracle_nan_gate_test.go` + `internal/stdlib/stringlib.go` +
> `testdata/fuzz/FuzzOracleDiff/{7dd267cdb3e0b40a,f988b149f1af8375}` + 文档
> （`README.md`、`docs/design/engineering.md`、
> `docs/design/p1-interpreter/12-testing-difftest.md`）。产品代码只有一处改动
> —— `string.find` 的 plain 路径改用 `bytes.Index`（第 17 节），是纯性能修复、语义不变；
> 中途按用户选择试改过 `formatLuaNumber` 与 NaN-boxing，两次都实测否决并 revert，
> 见第 13 节。

## 任务

2026-07-25 nightly-diff-fuzz 的 p1 腿撞出两个 `FuzzOracleDiff` crasher：

| issue | 输入 | oracle | wangshu | corpus |
|---|---|---|---|---|
| #184 | `print(string.format("NA%E",-(0%0)))` | `NANAN` | `NA-NAN` | `7dd267cdb3e0b40a` |
| #185 | `print(tostring(0%0))` | `-nan` | `nan` | `f988b149f1af8375` |

两者都落在 PR #181（issue #173）刚机制化的 NaN 符号已知差异区域，所以第一反应是
「#173 的 span 机制有两个漏洞要补」。先做探针，结论把问题性质改了。

## 本轮做了什么

### 1. 探针先把同族出口面整个测一遍，而不是只看撞到的两种写法

fuzz 撞到 2 种写法，但「NaN 数值 → 文本」的出口不止这两个。探针实测发现 **7 种
写法都完全没有 span 证据**（两侧 `spans` 都是空的）：`tostring(0%0)`、
`"x"..(0%0)`、`string.format("%s",0%0)`、`string.format("%q",0%0)`、
`string.rep(0%0,2)`、`table.concat({0%0,0%0},",")`、`(0%0).." "`。

根因是证据锚点挂错了层：`preludeCapture` 只在 `print` / `io.write` 直接见到
`type(v)=="number" and v~=v` 时记 span。凡是 NaN 在到达 print 之前就已经被转成
string 的路径，print 只看到一个普通 string，一格 span 都不记。

### 2. 探针进一步测出：NaN 符号差异会泄漏成纯数值差异

这一步改变了问题的性质。符号字符一旦进入字符串，就改变了字符串的长度和内容：

| 表达式 | oracle | wangshu |
|---|---|---|
| `string.len(0%0)` | `4` | `3` |
| `string.byte(0%0)` | `45`（`-`） | `110`（`n`） |
| `string.sub(0%0,1,1)` | `-` | `n` |
| `string.len(0%0)*100` | `400` | `300` |
| `string.find(tostring(0%0),"-",1,true)` | `1 1` | `nil` |

`print(400)` vs `print(300)` 里**没有任何 NaN token**，span-anchored 拼写分类在
结构上无法覆盖 —— 它的前提是「差异局限在 nan / NAN 那几个字节的符号列」。而且 4
和 3 是确定的、有语义的 Lua 数值，不像它来源的符号位那样「IEEE 754 不赋数值语义」。

结论：**#173 的「已知平台差异」定性，其适用边界比当时认定的窄**。对浮点转换
（`%e/%f/%g` 家族，走各引擎自己的 printf 代码）成立；对文本 coercion 不成立。
本轮是**收窄** #173 的定性，不是扩展它。

### 3. 方案摆给用户选，用户选 B

- **A**：让 wangshu 的 NaN 文本渲染对齐 x86/glibc（只改文本拼写，不动 NaN-boxing
  值层）。保住覆盖面，但把产品可见行为绑定到某个 libc。
- **B**：把 NaN 文本 coercion 整类判为不可比，两侧对称 skip。产品代码零改动，
  代价是丢这类输入的差分覆盖。
- **C**：只修 #184 的 span 错位，#185 家族挂起再问。

用户选 **B**。推荐理由（用户认可）：A 把引擎可见语义钉到某一个 libc 的拼写上，
等于用产品行为迁就 oracle；而且 arm64 glibc 对 `-(0/0)` 打 `-NAN` 与 x86 不同
（#173 的 CI 已实测过），对齐一个必然偏离另一个。

> 补记：第四轮之后用户重新考虑过 A，先选「改 `tostring(NaN)` 的拼写」、再选「真改
> NaN-boxing 保留符号位」，两次都被实测否决 —— 前者卡在值层的 NaN 规范化，后者卡在
> 硬件产生的 NaN 编码与 `TagNil` 逐位相同。所以 A 现在不是「不推荐」，是**不可行**，
> 见第 13 节。

### 4. 实现：`preludeNaNCoercion` 层

新增在 `internal/oracle/prelude.go`，追加在 `Prelude` 最后（trim 和 sorted-iter
之后），对称跑在两侧，见到 NaN 就抬共享的 `LimitSentinel`，两侧同时 skip。

**能拦的（走全局函数的出口）**：`tostring`、`string.len/sub/byte/upper/lower/reverse/rep`、
`table.concat`、`string.format` 的 `%s` / `%q`、以及 fmt 本身是 NaN 数值的情况。

**拦不住的**：`..` 操作符。读 vendored PUC 源码 `internal/oracle/_lua515/src/lvm.c`
的 `luaV_concat` 核实：它对 `ttisnumber` 操作数直接走 `tostring` 宏，不触发任何
元方法；wangshu 侧探针实测也绕过全局 `tostring`；`debug` 不在白名单，所以也无法
用 `debug.setmetatable` 给 number 挂 `__concat`。所以改在**输出端**兜底：
`__emit_nan_gate` 对没有 span provenance 的字符串，用本引擎自己的 NaN 拼写
（`local __nan_text = __tostring(0/0)`）做**大小写敏感的 plain 查找**（不是
pattern）。这样 `print("BANANA")` 只含大写 `NAN`，不会被误伤 —— 正是 #173
reviewer 提的那个经典负例。

> 注意：本节写的是审计**之前**的设计，其中「用本引擎自己的 NaN 拼写做 needle」这一
> 步是错的，见下面第 8 节 Blocking 1。而整个**输出端兜底**（`__emit_nan_gate`）在第
> 四轮已经**删掉**了：它在这个约束下不可能对称，见第 13 节。最终留下的只有入口拦截，
> `__gate1` 对 string 参数查固定小写 `nan`，所以「`..` 的结果传进被拦函数」仍然拦得住；
> 「`..` 的结果直接送进输出」改报对称差异。

### 5. #184 的 span straddle 修复

span 搜索从左往右扫，fmt 字面尾部恰为 `[nN][aA]` 且紧接浮点转换时，会与结果的
首字符 `n` / `N` 拼成一个**假 token**，锚点落在转换结果之前。oracle 的 `NANAN`
首个匹配在 offset 0，wangshu 的 `NA-NAN` 在 offset 3，gap 段 `""` vs `"NA"`
不等 → `OutputDifferent`。

这个错位天然不对称（只在省略符号的那一侧发生），所以跳过判据必须**只从 fmt
字符串推导**（两侧完全相同）才对称。判据：fmt 里某个浮点转换（`f/F/e/E/g/G`）
的 `%` 之前恰好两个字符是 `[nN][aA]`。1 个字符的字面后缀不可能造成这种拼接，
因为 NaN 渲染不会以 `a` / `A` 开头。

### 6. 自己踩的假绿：门层用被包装过的 pattern 引擎

第一版门里用 Lua pattern `"%%[%-%+ #0-9%.]*[sq]"` 匹配格式串的 `%s` / `%q`
verb，用的是 `string.find`。但门层跑在 `preludeGuards` 之后，那时 `string.find`
已经被 `__patcheck` 包装过，包装里有「量词字符超过 2 个就报 pattern too
branchy」的上限，我那个模式里有 3 个量词字符，立刻触发上限。结果是两侧都报
`oracle-harness: pattern too branchy`、输出都变成空串，于是 `CompareOutput`
判 `equal` —— **门完全没生效，但测试显示「相等」**。

发现靠的是打印实际 output 和 err：探针里三个 case 显示 `equal`，但 `out=""`
且 `err="oracle-harness: pattern too branchy"`。如果只看「没报 DIFFERENT」就会
漏掉。

修法两步：

1. `%s` / `%q` verb 检测改成手写逐字节扫描，完全不碰 pattern 引擎；
2. 需要 plain 查找的地方，把 `preludeGuards` 之前捕获的原始 `string.find` 通过
   一个 harness 私有全局 `__oracle_sfindraw` 传给门层（`writeTrim` 的 keep 列表
   里加上这个名字，门层用完立刻把它设为 nil，这样 fuzz 脚本看不到它）。

### 7. 验证

- 探针实测：泄漏面 15 种写法全部 `SKIP-both`（两侧对称跳过）；`%E` 系列恢复
  `knownNaN`（#173 通道仍活）；`print("BANANA")` / `print("NAN")` /
  `print(string.len("BANANA"))` 仍 `equal`。
- 邻近但不该跳的形式实测仍走 knownNaN：`"A%E"`、`"N%E"`、`"NAx%E"`、`"%E NA"`、
  `"%En"`、`"%Ena"`、`"%E%E"`、`"n%f"`、`"a%f"` —— 证明 straddle 判据精确，没有
  过度牺牲覆盖面。
- 两个原始 crasher 都 SKIP。
- 反向验证：把门层从 `Prelude` 里摘掉 + 去掉 `__emit_nan_gate` 调用，
  `f988b149f1af8375` 立刻恢复原样的 divergence 失败，证明门是真的起作用
  （load-bearing）。
- `go build ./...` + `go vet ./...` 干净；`go test ./internal/oracle/...` 带与
  不带 cgo tag 都过；全部 45 个 corpus 条目过。
- 90 秒 fuzz smoke（`-parallel=4` 按共享机规则）46840 execs 零 crash，
  `new interesting: 12` 说明门生效后 fuzz 仍在探索新覆盖、没被卡死。中途有约
  35 秒 exec/sec 掉 0，查 `uptime` 是共享机 load 5.96 / 24 核 / 13 用户在线，
  是别人的负载，不是门导致 —— 全量语料 0.3 秒跑完，没有慢路径。
- 新增 `TestExec_NaNCoercionGate`（20 个必须跳 + 13 个必须保持可比）和
  `TestExec_NaNGateSymmetry`（钉住跳过决定不依赖单侧拼写）。
- 原 `TestExec_NaNSpans` 里的 `print(string.format(0/0))` 用例移除（fmt 本身是
  NaN 现在归入被拦的类），其约束以显式跳过预期的形式保留在新测试里。

> 这一节看起来很充分，但下面第 8 节的独立审计在同一份代码上找出 8 个成立的问题，
> 其中 3 个 blocking。这份验证清单漏掉的正是「我没想到的那些输入类」，见教训 7。
> 修完那 8 条之后我又写了一份同样自信的验证清单，第 9 节的增量盲审又找出 6 条，
> 其中 1 条是修复自己引入的回归；再修完之后第 10 节的全范围盲审又找出 6 条，其中
> blocking 是同一类错误的第三个实例，藏在第一轮就写下的代码里；第 11 节的枚举测试
> 写完之后，第 12 节的第四轮盲审又找出 6 条，其中 blocking 之一是第三轮那个修复只
> 修了一半。四次同类之后我才去问「这个机制能不能做对」，答案是不能，机制已删除
> （第 13 节，教训 1）。机制删掉之后还有两轮：第 15 节的第五轮抓出这类缺陷的第五个
> 实例（又是我修上一条时引入的，而这次我终于按教训 1 去问前提），第 16 节的第六轮
> 抓出一个全新维度 —— 判据的**运行成本**两侧不同。之后还有二十轮（第 17 节），26 轮里
> **13 轮抓到的缺陷是我修上一轮时引入的**，而缺陷集中在两处抽象上：吸收逻辑改了六版、
> 「限制扫描规模」失败五次。

### 8. 第一轮独立审计：盲审 8 条，全部成立

上面那一节写完、自认为验证充分之后，用 close-local-code-review 技能做了一次独立
本地审计。做法：在固定 SHA 的隔离快照 clone 里跑（`bwrap --unshare-net` 的断网
边界实测验证过），启动一个无继承上下文的盲审 reviewer，只给它仓库规则、任务目标、
固定范围，以及它自己在快照里跑出来的原始证据；不给父对话、不给预期结论、不给我
心里的可疑点。

盲审结论 **REQUEST_CHANGES：3 blocking / 3 important / 2 minor**。我逐条用双侧
探针核实，**8 条全部成立**。

#### Blocking 1：needle 用了本引擎自己的拼写，判据在两侧分叉

`__emit_nan_gate` 的 needle 我写成了 `__nan_text = __tostring(0/0)` —— 本引擎
自己的 NaN 拼写。后果：PUC 侧的 needle 是 `"-nan"`（4 字节），wangshu 侧是
`"nan"`（3 字节）。**同一个脚本，两侧判据不同。**

实测：`print("banana")` 在 wangshu 侧抬 sentinel（`banana` 含 `nan`）、oracle 侧
正常输出 → **单侧 skip**。而 `FuzzOracleDiff` 的逻辑是「任一侧 `VerdictLimit` 就
`t.Skip`」，所以这个输入连同**同一次运行里所有真实差异**一起被静默丢掉。审计给的
实证是 `print("banana") print(string.len(""..(0/0))*100)`：后半句是活的
400-vs-300 差异，加上前半句整个输入就消失了。

**单侧 skip 比误报严重**：误报会亮红灯，单侧 skip 是静默失效。

我自查时的错误在于只验了大写 `BANANA`（因为 #173 的 reviewer 提过那个负例，我一直
盯着它），没验小写的普通英文单词。`banana` / `finance` / `nanosecond` / `tenant` /
`covenant` 全中。

修法：needle 改成**固定的小写字面量 `"nan"`** —— 两侧的强制转换结果都含小写 `nan`
字节，所以固定 needle 在两侧一致。更尖锐的判据（词边界 `%f`、符号前缀、精确长度）
都试过并**实测否决**：它们都要比较两侧**不同的**强制转换文本，必然重新引入单侧
skip。我在这上面来回试了三轮才想清楚：凡是基于「字符串的样子」的判据，在两侧输出
本就不同的场景下都不可能对称；只有「引擎无关的固定字面量」或「由两侧同样数据算出
的运行级标记」才行。

#### Blocking 2：名字叫 symmetry 的测试在结构上测不了 symmetry

`TestExec_NaNGateSymmetry` 写在 `internal/oracle` 包里，而这个包的 `execT` 只跑
cgo PUC shim，从不实例化 wangshu。名字叫 symmetry，实际只能看一侧。它在快照上是
PASS 的，而 `print("banana")` 那个单侧 skip 就在旁边。

这正是我在这篇反思里当成头条写的那类错误（判据无法观察它声称的东西），我自己在
同一轮里又犯了一次 —— 而且是在刚写完那条教训之后。

修法：改名 `TestExec_NaNGateEngineIndependentNeedle`，缩到它真能检查的范围，并在
注释里写明为什么这个包做不到 symmetry；真正的两侧断言移到根包新文件
`oracle_nan_gate_test.go`（只有根包同时驱动两个引擎）：
`TestFuzzOracleDiff_NaNGateSymmetry`（任何脚本不得一侧 limit 一侧正常）/
`NoFalsePositive` / `AcceptedCost` / `Coverage`。

#### Blocking 3：漏掉一整组 coercion 出口，而文档声称已覆盖

漏了 `string.find` / `match` / `gmatch` / `gsub` —— subject 参数走同一个
`luaL_checklstring` 强制转换。实测 `string.match(0/0,"^.")` 是 `-` vs `n`、
`string.find(0/0,"n")` 是 `2 2` vs `1 1`、`string.gsub(0/0,"n","X")` 是
`-XaX` vs `XaX`。

更关键的一点：`..` 的结果作为 **string 参数**流进 `string.len` / `byte` 时，它
已经是普通 string（带着符号），压成数字后输出里没有 NaN 字节，输出端兜底原理上
看不见。`string.len(""..(0/0))*100` → 400 vs 300 **至今仍是活的** —— 而这正是我
写进 godoc 和三份设计文档、声称已处理的那个例子。文档说已覆盖，实际没有。

#### Important 4 / 5 / 6 与 Minor 7 / 8

- **大写跨 concat straddle**：`print("NA" .. string.format("%E", -(0/0)))` 仍报。
  `format` 结果有 provenance，但 `..` 产生**新字符串**没有 provenance，大写 `NAN`
  被小写 needle 看不见。修法：加 `__fmt_nan_rendered` 标记（仅当 format 的浮点
  转换真渲染了 NaN 时置位，判据只用 fmt 字符串 + 参数类型，所以两侧一致），置位后
  做大小写不敏感扫描。**刻意不由 `print` / `io.write` 见到裸 NaN 来置位** —— 那会
  让 `print(0/0, "BANANA")` 被跳过，丢掉 #173 reviewer 给的那个关键负例。
- **`string.format` 拦过头**：只要 fmt 里有 `%s` 就拦所有 vararg，于是
  `string.format("%s %E","x",-(0/0))` 被误跳（NaN 落在 `%E`，那条路 #173 仍覆盖）。
  修法：按顺序走每个转换，只拦 `%s` / `%q` 实际消费的参数位。
- **`table.concat` 的检查有副作用**：用 `t[k]` 读元素会触发脚本的 `__index`，而真
  `tconcat` 用 `lua_rawgeti` 不触发，副作用会进入被比较的输出。修法：改 `rawget`。
- **`tostring` 重绑改了签名**：我重绑成 `function(v)`，把无参调用从报错变成返回
  `"nil"`。修法：保持 varargs 签名。
- 注释引用了不存在的标识符 `nanCoercionSentinel`。

> Important 4 那条修法（`__fmt_nan_rendered` 标记 + 大小写不敏感扫描）在第二轮增量
> 盲审里被判为引入回归，已整段删除，见下面第 9 节。

### 9. 第二轮增量盲审：1 blocking 是我自己在第一轮修出来的

第一轮 8 条修完之后，我又做了一次**增量盲审**：新的隔离快照 clone、`bwrap
--unshare-net` 断网边界实测验证过、新的无继承上下文 reviewer，范围只看第一轮那三个
修复 commit（`4d45dad..d0eaba1`）。

结论 **REQUEST_CHANGES：1 blocking / 2 important / 3 minor**。逐条核实，**又是全部
成立**。

#### Blocking 1：修第一轮问题的过程中引入了新的同类缺陷

第一轮审计的头条是「needle 用了本引擎自己的拼写导致单侧 skip」。我在修它的时候，
为了顺手覆盖大写跨 concat straddle（`print("NA" .. string.format("%E", -(0/0)))`），
加了一个 `__fmt_nan_rendered` 标记 + **大小写不敏感扫描**。

我当时的推理是：标记本身由两侧相同的数据（fmt 字符串 + 参数类型）算出，所以对称。
**这个推理漏了一步** —— 标记是对称的，但扫描的**对象**不对称：扫描读的是运行时字符串
**内容**，而内容两侧不同（oracle 侧 `%E` 得 `NAN`，wangshu 侧得 `-NAN`）。

审计构造出两个方向的实证，偏移差 1 就翻转：

| 输入 | oracle | wangshu |
|---|---|---|
| `print((string.format("%E",-(0/0)).."Z"):sub(1,4))` | Limit | OK（输出 `-NAN`） |
| `print((string.format("%E",-(0/0)).."Z"):sub(2,5))` | OK（输出 `ANZ`） | Limit |

我实测复现了两条，确认成立。审计还在 base commit 上跑了同样两条，分类是
`different` 而不是 `skip-asymmetric`，**证明这是我这一轮引入的回归**，不是原有边界。

审计另外指出：我新写的 `TestFuzzOracleDiff_NaNGateSymmetry` 断言的正是「任何脚本不得
一侧 limit 一侧正常」，它 PASS 只是因为**用例表里没有这个形式**。

最讽刺的是同一个函数的注释是我自己写的，里面明明写着「更尖锐的判据都试过并否决，
因为它们要比较两侧不同的强制转换文本；单侧 skip 比丢覆盖更糟」。第一个检查（固定
小写 needle）遵守了这条规矩，我紧接着加的第二个检查违反了它，位置就在那段注释下面
几行。

修法：删掉扫描和标记。大写跨 concat straddle 现在报**对称的差异**，归入已知边界。
取舍写清楚 —— 宁可报一个对称差异（会亮红灯、人能看到），也不要单侧 skip（静默吞掉
同一次运行里的真实差异）。

#### Important 2：`__gate1` 的判据太窄

我把它写成「string 参数**整串**恰为某个 NaN 拼写」。`..` 的结果带任何前后缀就绕过，
实测 8 种写法全部未拦：

| 表达式 | oracle | wangshu |
|---|---|---|
| `string.len("x"..(0/0))` | `5` | `4` |
| `string.sub("x"..(0/0),1,2)` | `x-` | `xn` |
| `string.upper("x"..(0/0))` | `X-NAN` | `XNAN` |
| `string.find("x"..(0/0),"a")` | `3 3` | `2 2` |
| `string.match` / `string.rep` / `table.concat` 各自的带前后缀写法 | 带 `-` | 不带 |

审计特别指出：这使我新写的注释断言「cover **every** shape that routes through a
library function」和 `TestFuzzOracleDiff_NaNGateCoverage` 的口径**不成立**，而三份
设计文档照抄了这个口径，把已知边界写得比实际窄。

修法：改用与 emit 检查相同的固定小写 needle 做 plain 查找。

#### Important 3：把测试改成不再观察它名字所指的东西

第一轮我把 `TestExec_NaNSpansKnownLimit` 改写成只断言 `VerdictLimit`，于是它不再验证
「FIFO provenance 被 literal 提前消费导致 span 错位」—— 那个原始的 known limit
**没有任何测试看着了**，而函数名和 `prelude.go` 里的注释引用都没跟上。

修法：改回去观察真正的错位（唯一那个 span 落在 literal 上、offset 0）。改的过程中
还发现那些 literal 是**大写**的、needle 是小写的，所以这个 collision 窗口**本来就
还开着** —— 第一轮说它「已被扫描拦住」是错的。

#### Minor 3 条

误缩进；注释仍写 needle 是本引擎拼写并指向已改名的测试；`print(string.upper("nan"))`
从 comparable 翻转成 accepted cost 但没在 `AcceptedCost` 用例表补钉。

### 10. 第三轮全范围盲审：同一类错误的第三个实例

第二轮修完之后又做了一次盲审，这次范围**回到完整的 `c982610..92241f4`**（不再只看增量）：
新的隔离快照 clone、`bwrap --unshare-net` 断网边界实测验证过、新的无继承上下文
reviewer。结论 **REQUEST_CHANGES：1 blocking / 2 important / 3 minor**，逐条核实，
**仍是全部成立**。

#### Blocking 1：provenance 的 key 里藏着本引擎的拼写

`__nan_provenance` 之前用 coerced 字符串**本身**作 key。那个字符串两侧不同（PUC 侧
`-nan`、wangshu 侧 `nan`），所以同一个 `string.format` 调用在两侧被记到**不同的 key**
下。而 `__emit_nan_gate` 的豁免判断要查这张表，于是「format 结果算出来但没被 print
出去」的时候，provenance 一直挂着、且挂的 key 每侧不同：

| 输入 | oracle | wangshu |
|---|---|---|
| `local _ = string.format("%e", 0/0) print("nan")` | Limit | OK |
| `local _ = string.format("%e", 0/0) print("-nan")` | OK | Limit |

审计还证明它会吞掉**同一次运行里的真差异**。触发面不窄：format 结果被丢弃、进
`pcall`、进表构造器、做条件判断，都属于「算出来但没被消费」。

我第二轮新写的 `TestFuzzOracleDiff_NaNGateSymmetry` 漏掉它，原因很具体：那 30 条用例里
每个 format 结果都被**直接 print 消费**了，所以 provenance 总是立刻用掉、不留残留。

修法：`__prov_key(s)` 剥掉一个前导符号之后再作 key，两侧落进同一个桶。

**这是同一类错误的第三次**：① needle 用本引擎拼写；② 对称标记作用在不对称的扫描对象
上；③ provenance key 里藏着本引擎拼写。三次的共同点是「判据的某个输入其实是两侧不同
的量」，而且每次我都以为已经想清楚了。

#### Important 1：我加的检查把一条既有的真差异藏了起来

`table.concat` 的预扫跑在真 `tconcat` 校验参数**之前**。非数值的 `i` / `j` 会先触发
预扫里的数值 `for`，把 `tconcat` 自己的 `bad argument #4` 盖掉 —— 而那条消息两侧本来
有真差异，于是**真差异被掩盖成 `equal`**。另外 `j` 给 `1e15` 会让预扫真的循环起来，
两侧各烧数百毫秒撞预算判 skip，而 base 上 1ms 就报出了真差异。

这是一种和判据不对称不同的失效模式：判据本身对称，但它**有副作用** —— 改变了错误发生
的顺序、错误的文本，以及执行时间。

修法：只在参数已经是 `tconcat` 能接受的形式时才预扫，范围钳到 `#t`。

#### Important 2 与 Minor 3 条

- `nan_straddle` 的注释把覆盖面说得比实际宽：实际只处理单个 fmt 字符串内的**前缀**
  方向；后缀方向（`%en` / `%ean`）和字面文本经**参数**传入
  （`string.format("%s%E","NA",-(0/0))`）都不覆盖。
- `__gate` helper 已经没有调用点。
- 分层注释写「every number->string coercion reachable from Lua」，比实际范围宽。
- 一处误缩进。

### 11. 不等下一轮盲审：把教训 2 做成可执行的东西（维度选错，见第 12 节）

三轮审计各抓一个同类实例之后，我没有再等下一轮盲审，而是**按教训 2 自己说的做了一次
机械枚举**：把 gate 路径上每一处「读取某个值」的地方 `grep` 出来（`__sfindraw` /
`__nan_provenance` / `__prov_key` / `__isnan` / `__nan_needle` / `#s` 等全部读取点），
逐个问「这个值两侧相同吗」。

然后把这次枚举变成常驻回归测试 `TestFuzzOracleDiff_NaNGateNoAsymmetricSkip`（根包，
只有根包同时驱动两个引擎）：**29 种写法**，覆盖 provenance 登记后未消费、乱序消费、
同值产生两次只消费一次、被 `..` 拼接、被丢进 `pcall` / 表构造器 / 条件判断、以及与两
种拼写的脚本字面量碰撞。断言是「任何输入不得只在一侧 skip」。

**反向验证**：把 provenance key 改回按值索引，这个测试报
`7/29 shapes decide asymmetrically` —— 证明它真的起作用，不是装饰。

结果：29 种写法零不对称。两个 `DIFFERENT` 是已知边界（`#a`、`a == "nan"` 消费 coerced
文本时没有经过任何被拦函数）。

这一节和前三轮的关系是分工：**审计提供的是独立视角**，它不知道我想到了什么，所以三轮
各撞出一个我看不见的实例；**枚举测试把这个视角固化下来**，写进 CI 之后，下一次同类错误
不需要等一轮审计才被发现。教训 2 里写的「需要机械化的逐条检查」这次真的做成了可执行的
东西，而不是只写在反思里。

> 后续修正：第四轮盲审证明这次枚举**维度选错了** —— 29 条里 `string.format` 的 fmt 全是
> 裸 verb，恰好是当时那个 key 唯一能正确归一化的写法，加一个字面前缀立刻报不对称。反向
> 验证也只验了「旧的按值 key」这一种缺陷，没验「前缀 fmt」这一种。见第 12 节和教训 8。
> 更上一层：四次同类缺陷说明该做的不是把枚举补全，而是证明这个机制能不能做对（教训 1）。

### 12. 第四轮全范围盲审：上一轮的修复只是部分修复，枚举测试的维度选错了

第三轮修完 + 枚举测试写完之后又做了一次全范围盲审：新的隔离快照、新的无继承上下文
reviewer、完整范围 `c982610..a7197a8`。结论 **2 blocking / 2 important / 2 minor**，
逐条核实，**全部成立**。

#### Blocking 1：`__prov_key` 只剥第 1 字节，所以只是部分修复

第三轮的修法是「`__prov_key(s)` 剥掉一个前导符号后作 key」。问题在于我剥符号**只看第 1
字节**。PUC 的 `-nan` 只有在 nan token 紧贴串首时符号才落在第 1 字节；fmt 一旦带字面
前缀或宽度 padding，符号就落到串中间，两侧的 bucket key 又不同了。实测 4 种单侧 skip：

| 输入 | oracle | wangshu |
|---|---|---|
| `local a = string.format("x%e", 0/0) print("xnan")` | Limit | OK |
| `local a = string.format("z%e", 0/0) print("z-nan")` | OK | Limit |
| `local a = string.format("%5e", 0/0) print(" -nan")` | OK | Limit |
| `local a = string.format("%e %e",0/0,0/0) print("nan nan")` | Limit | OK |

#### Blocking 2：span 长度按记录方引擎的 token 记，越界后 PUC 侧判 invalid readout

span 长度是按**各自引擎的 token** 记的（PUC 侧 4 字节，含符号）。被一个 3 字节的同值
字面量提前消费掉之后，span 末端越过 body 的长度，`DecodeOutput` 判 `ok=false` → PUC 侧
得到 `VerdictLimit: invalid readout`，wangshu 侧正常。实测
`local a = string.format("%e", 0/0) io.write("nan")`，`%g` / `%f` 一样。

也就是说第 10 节那个「FIFO 提前消费」的已知限制，后果不止是 span 落到错误偏移，还会在
一侧变成 limit —— 又是一次单侧 skip。

#### Important：我的 29 条枚举测试栽在「维度选错」上

我上一轮写的 29 条枚举测试里，`string.format` 的 fmt **全是裸 `%e` / `%E` / `%f` /
`%g`** —— 恰好是 `__prov_key` 唯一能正确归一化的写法。加一个字面前缀，测试立刻报不对称。

**我以为自己做了「机械枚举」，其实枚举的维度选错了**：我枚举的是「provenance 的消费
时序」（登记未消费、乱序消费、同值产生两次只消费一次、进 pcall / 表构造器 / 条件判断），
没有枚举「fmt 的写法」（裸 verb / 带字面前缀 / 带宽度 padding / 多个 verb）。而缺陷正是
沿后一个维度变化的。

我做过反向验证，但只验了「旧的按值 key」那一种（报 `7/29 shapes decide
asymmetrically`），没有验「前缀 fmt」那一种。反向验证只能证明测试对**我构造的那个**已知
缺陷有效，不能证明它对同一机制上的其他缺陷有效。

#### Important 与 Minor 的其余几条

- `preludeGuards` 里 `fmt_was_nan` 分支已经不可达：gate 层最后包了 `string.format`，
  `__isnan(f)` 会先抬 sentinel，所以数值 NaN 的 fmt 永远走不到 guards 那一层；分支和
  22 行注释还留着。
- `__nan_needle` 与 `__gate1` 里的 `"nan"` 是**两份独立的字面量**，而注释称它们是「同一
  个」needle。
- `string.gfind = string.gmatch` 在 trim 之后无条件赋值，绕过了白名单。

### 13. 不再修补：先证明机制能不能做对，再由用户在产品侧两个选项上各实测一次

第四次同类缺陷之后我没有再补 key，而是去问一个不同的问题：**这个机制能不能做对。**
结论：**不能**，而且证明只需要一步。

要保住 #173 的 span 分类，输出端检查就必须**豁免**浮点转换的结果（否则 `%e` 那条路
也会被拦掉）。而唯一可用的豁免判据是「这个字符串有没有 provenance」—— Lua 5.1 没有
string identity primitive，只能按字符串的**值**查表。而那个值恰恰就是两侧的差异
（`-nan` / `nan`）。所以脚本里任何一个恰好等于某一侧拼写的字面量，只会在那一侧命中
豁免。

四轮的四个缺陷 —— ① needle 用本引擎拼写、② 对称标记作用于不对称数据、③ 按值索引的
provenance key、④ 只剥第 1 字节的 key —— **都是同一个不可对称的 key 的变体**。这一步
证明本该在第二轮就做，前三轮我都在「把 key 修得更聪明」。

#### 用户先选「改产品侧」：改 `formatLuaNumber` 无效，根因在值层

我把选项摆给用户，用户先选「改 wangshu 的 `tostring(NaN)` 输出 `-nan`」。我改了
`formatLuaNumber`（单一 choke point，全部默认测试通过），但 wangshu 仍然输出 `nan`。

根因在上游：`internal/value/value.go` 的 `NumberValue` 把**所有** NaN 规范化为单一的
`canonNaN`（NaN-boxing 的地基，`IsNumber` 的边界测试依赖它，仓库里 186 处引用）。符号
位在值层就被丢掉了，渲染层改不回来。

#### 用户再选「真改 NaN-boxing 保留符号位」：spike 实测，硬件层面不可行

这是本轮最有长期价值的技术结论。以后任何人再提「让 wangshu 对齐 PUC 的 NaN 拼写」，
都应该先读这一段。

- x86 上**每一个**产生 NaN 的浮点运算 —— `0/0`、`inf-inf`、`0*inf`、`inf/inf`、
  `sqrt(-1)`、Lua 取模用的 `x-floor(x/y)*y` —— 都返回 `0xFFF8_0000_0000_0000`，与
  `TagNil` **逐位相同**。
- 负 quiet NaN 的最小编码恰好等于 `IsNumber` 的边界 `0xFFF8_...`（`qNanBoxBase`），
  所以负 quiet NaN 在这个布局里**没有容身之处**。
- 负 signaling NaN（`0xFFF4_...`）确实落在 number 空间内，实测它能穿过赋值、slice、
  map、interface 而不被 quiet 化。但**硬件从不产生它**：要用就得在 P1 解释器、P3 发射
  的 wasm、P4 发射的 amd64 与 arm64 **四条代码生成路径**上，对每一个浮点运算的结果做
  重映射，漏一处 NaN 就会被读成 `nil`。而且 `float32` 窄化会把它变回 `0xFFFC...`，
  再次撞进 tag 区。
- 顺带实测出：**PUC 本身也不是固定 `-nan`**，它跟随符号位 —— `0/0` → `-nan`、
  `-(0/0)` → `nan`、`tonumber("nan")` → `nan`。所以硬编码 `-nan` 会修好第一个、
  弄坏后两个。

我 revert 了产品侧的改动，把这些实测结果报给用户。

#### 最终决定（用户选）：删掉整个输出端检查

commit `2613871`。保留：

- **入口拦截**（`tostring`、`string.len/sub/byte/upper/lower/reverse/rep`、
  `string.find/match/gmatch/gsub`、`table.concat`、`string.format` 的 `%s`/`%q`）。
  `__gate1` 对 string 参数查固定小写 `nan`，所以「`..` 的结果传进被拦函数」仍然拦得住。
- **#173 的 span 通道**（浮点转换那条路完全没动）。
- **#184 的 fmt 字面 straddle 规则**。

两个 issue 仍然解决：#185 靠 `tostring` 入口，#184 靠 straddle 规则。

`..` 的 coercion 直送输出改为**报对称差异** —— nightly 能看到、按已知边界处理，而不是
静默丢掉输入。

**换回来的收益**：之前被输出端检查误跳的普通文本恢复可比 —— `print("banana")`、
`print("finance")`、`print("nan")`、`print("-nan")`。删掉一个覆盖不全又不可能对称的
机制，同时拿回了一片真实的覆盖面。

### 14. 已知边界（用户决定接受）与已接受的代价

最终口径（第四轮删掉输出端检查之后）：**凡是 coerced NaN 文本没有经过任何被拦函数的
路径，都报对称的差异**，不再尝试拦。这比前三轮的表述宽 —— 之前是「`..` 的结果被 `#`
或 `==` 消费」这一小片，现在把「`..` 的结果直接送进输出」也包含进来了：

- `#(""..(0/0))` → 4 vs 3、`#(""..(0/0))*100` → 400 vs 300；
- `(""..(0/0)) == "nan"` → false vs true；
- `print(""..(0/0))` → `-nan` vs `nan`（这一条前三轮由输出端检查对称跳过，现在改报差异）；
- `print((string.format("%E",-(0/0)).."Z"):sub(2,5))` → `ANZ` vs `NANZ`。

要拦这些就必须读 coerced 字符串的内容，而内容两侧不同 → 必然单侧命中。第 13 节把这一
点证明成了结构性结论，不是「还没想到办法」：豁免判据只能按字符串**值**查表，而值就是
差异本身。所以这条边界是**判据对称性的直接推论**，不只是宿主语言少个 hook。

**报对称差异是刻意的选择**：nightly 能看到、按已知边界处理，比静默丢掉输入好。之前那个
输出端检查换来的覆盖是假的 —— 它拦不住 `#` / `==` 那一片，却顺手把 `print("banana")`
这类普通文本跳掉了。

两条 hook 缺失都读了 vendored PUC 源码核实：

- `internal/oracle/_lua515/src/lvm.c` 的 `luaV_concat` 对 `ttisnumber` 走
  `tostring` 宏，不触发元方法 → `..` 拦不住；
- 同一个文件里 `OP_LEN` 对 `LUA_TSTRING` 直接读 `tsvalue(rb)->len`，也不触发元
  方法 → `#` 也拦不住。

两者串起来把符号差异压成纯数字，输出里不留 NaN 字节，prelude 没有任何观察点。

这是 Lua 5.1 语义 + 判据对称性共同决定的硬边界，不是实现疏漏。产品侧那两个选项
（改 `tostring(NaN)` 的拼写 / 改 NaN-boxing 保留符号位）用户都选过，都被实测否决，
见第 13 节。已用测试固化：`TestExec_NaNCoercionGateKnownBoundary` 和
`TestFuzzOracleDiff_NaNGateCoverage` 第二段都断言这些写法**不被拦**，让边界公开
可见、也不会悄悄扩大。

**已接受的代价**（第四轮之后只剩入口拦截这一份）：`__gate1` 用固定小写 `nan` 查 string
参数，所以把小写 `nan` 当普通词**传进被拦函数**也会被跳过 ——
`string.len("banana")`、`string.upper("nan")`、`table.concat({"a"},"nan")` 这类。这是
**对称的**：两侧都跳，丢的是输入，不产生错误判定。大写不受影响。用
`TestFuzzOracleDiff_NaNGateAcceptedCost` 固化。

**第四轮拿回来的覆盖面**：输出端检查删掉之后，普通文本只要不进被拦函数就恢复可比 ——
`print("banana")`、`print("finance")`、`print("nan")`、`print("-nan")`、
`print("BANANA")`、`print(0/0, "BANANA")` 全部正常比对。前三轮那份代价里最大的一块
（任何含小写 `nan` 的输出都被跳）已经不存在了。

> 第六轮补记：这份清单还缺三条本来就成立、只是没写出来的边界，已补进 `README.md` 与两份
> 设计文档（commit `55d880c`）—— coerced 文本进到 **pattern** 参数、**函数或表形式**的
> `gsub` 替换（结果在 `gsub` 内部产生，包装层看不见）、以及**大写** coerced 文本经被拦
> 函数（needle 只能是小写）。「覆盖每一种经库函数的写法」这个口径也随之再收窄一次。

### 15. 第五轮全范围盲审：第五个同类实例，又是我修上一条时引入的 —— 这次按教训 1 做了

第四轮删掉输出端检查之后又做了一次全范围盲审（新的隔离快照 clone、`bwrap --unshare-net`
断网边界实测验证、新的无继承上下文 reviewer，完整范围）。结论 **1 blocking / 2 important
/ 1 minor**，逐条核实，**全部成立**。

#### Blocking：`#t` 是两侧不同的量，于是钳范围的预扫本身变成引擎相关的判据

`table.concat` 的 NaN 预扫把范围钳到 `#t`。而**带空洞的表 `#t` 在 Lua 5.1 里是未规定的**，
两侧引擎取值不同，于是跳过决策变成引擎相关：

| 输入 | oracle | wangshu |
|---|---|---|
| `local t={1} t[4]=4 t[2]=0/0 print(table.concat(t, ","))` | Limit（`#t`=4，NaN 在范围内） | OK（`#t`=2，NaN 在范围外） |

实测确认是单侧，也确认它会吞掉同一次运行里的真差异。

**关键在于这个预扫的来历**：它是我在**第三轮为了修另一条 important 而加的**（那条
important 说预扫有副作用，会把 `tconcat` 自己的 `bad argument #4` 顶掉，修法就是「只在
参数已经是 `tconcat` 能接受的形式时才预扫，范围钳到 `#t`」）。所以这是这一类缺陷的
**第五个实例，而且又是我修上一条时引入的**（第二轮那次也是这样，见第 9 节）。

#### 修法：这次终于不修算法，改问前提 —— 结果就变简单了

前四轮我做的都是「把判据算得更聪明」。这一次按教训 1 的做法，问的是**这个预扫的前提对不
对**：预扫要复制 `tconcat` 自己的遍历，就必须**猜它的边界**；而默认边界是 `#t`，`#t` 在
带空洞的表上无法两侧一致地猜。前提不成立，所以不该继续改算法。

换掉前提之后方案反而更简单：**检查结果字符串**。`tconcat` 实际读了什么，全都在它的返回值
里，完全不需要知道边界；判据退化成与别处一样的那个固定小写 needle，两侧求值必然相同。

这是教训 1 **第一次真的被用上，而且立刻见效** —— 与前四轮「一直在修算法」形成对照。

#### 两条 important：漏掉的两个参数位置

- **`string.gsub` 的 replacement 参数从未被检查**：`string.gsub("abc","b",0/0)` 把 coerced
  文本直接放进结果。之前只看了 subject 和 pattern。
- **`%s` / `%q` 槽位检查用的是 `__isnan`**：只认 NaN **数字**，看不见 `..` 已经转好的
  **字符串**。

两处都改走 `__gate1`（同一个固定小写 needle），判据统一。

#### 枚举测试的补强：补上三个维度，29 → 47 条

审计还指出我那 29 条枚举里的两个盲区：`string.format` 的 fmt **全是裸 verb**，表**全是
无空洞的** —— 恰好是当时那个缺陷能正确处理的写法。这与第四轮的 important 是同一件事的
第二次（见教训 8）。

补了三个维度：

- **fmt 写法**：字面前缀 / 宽度 / padding / 多个转换；
- **表写法**：空洞 / 间隙 / 稀疏；
- **参数位置**：`gsub` 的 replacement / `%s` 与 `%q` 槽位。

现在 47 条。**反向验证**：把 `#t` 预扫改回去，新表报 `3/47 shapes decide asymmetrically`，
而**旧的 29 条一条都报不出来** —— 直接量化了「维度选错的枚举有多弱」。

#### Minor

一处注释还在提已删除的 `__emit_nan_gate`。

### 16. 第六轮全范围盲审：一个全新的维度 —— 判据的运行成本两侧不同

第五轮修完之后再做一次全范围盲审（新快照、新的无继承上下文 reviewer、完整范围）。结论
**1 blocking / 2 important / 3 minor**，逐条核实，**仍是全部成立**。

#### Blocking：单侧 skip 经由时钟而不是经由值

`__sq_arg_slots` 逐字节扫格式串。构造一个 80 KiB 的 fmt，**既不含 NaN 也不含任何转换**，
调用 60 次：

| | verdict | 耗时 |
|---|---|---|
| PUC（oracle） | OK | 392ms |
| wangshu | `VerdictLimit`（`instruction budget exceeded`） | 1m22s |
| base 两侧 | 都 OK | 131ms / 216ms |

输入被整条丢掉。

**这是第六个同类实例，但维度是新的**：前五个都是「判据读的**值**两侧不同」，这个是
「判据**自身的开销**两侧不同」。根因在 harness 的结构上 —— 每个检查在 wangshu 侧是**解释
执行的 Lua**、在 PUC 侧是 **C**，所以任何开销随输入规模增长的检查，都会先把一侧推到撞
预算。后果与前五个一样是单侧 skip，只是经由时钟而不是经由值。

修法两步，两步都让成本跟着「转换个数」而不是「fmt 长度」走：

1. 扫描改用 plain find **跳到下一个 `%`**，复杂度 O(fmt 长度) → O(转换个数)；
2. 扫描（含 straddle walk）**只在「确实可能有参数被拦」时启动** —— 参数含 NaN 数字，或含
   已带符号的 coerced 字符串。两个判据都是每参数 O(1)，且由**参数类型**推导，所以两侧
   一致。

实测 38s → 264ms，与 base 的 216ms 同一档。

#### 成本测试自己踩的坑：断言选错了量，把 bug 改回去照样通过

新增 `TestFuzzOracleDiff_NaNGateNoCostAsymmetry`。第一版只断言「两侧 verdict 一致」，把
bug 改回去它**照样通过** —— wangshu 慢了 **74 倍**，但仍在预算内跑完，两侧 verdict 于是
相同。

必须断言的是**成本比值**，因为那才是「输入再大一点就变成单侧 skip」的那个量。verdict 只
是比值越过某个阈值之后的离散后果，用它做断言等于要求测试输入刚好落在阈值另一侧。

写法上有一处必要的设计：**每个写法与自己的短输入版本比**，而不是与绝对时间或与对侧的绝对
时间比。这样共享机器的负载同时影响比值的两端，不影响结论（本仓的共享机纪律要求这么写）。
**反向验证**：把逐字节扫描改回去，测试报 **645 倍 / 上限 120 倍**并失败。

#### 两条 important

- **`TestExec_NaNGateEngineIndependentNeedle` 之前没有它声称的判别力**：把 `__gate1` 的
  固定小写 needle 换成注释里**明令禁止**的 `__tostring0(0/0)`（本引擎自己的拼写），测试
  **照样通过**。原因是用例表两头都不判别 —— 正向用例在 PUC 侧都含 `-nan`，两种 needle 都
  命中；负向用例经 `print`，根本不过任何被包装函数。补了
  `print(string.len("banana"))`（只有固定小写 needle 会拦它），并验证 needle 退化时测试
  真的会失败。这与教训 8 是同一类，只是这次栽在**负向用例不经过被测路径**上。
- **coerced 文本进到 pattern 参数仍两侧不同**：它会在两侧**不同的点**触发 `__patcheck` 的
  量词上限 —— `string.find("x","**"..(0/0))` 在 PUC 侧 pattern 是 `**-nan`，3 个量词字符
  超上限；wangshu 侧是 `**nan`，正好 2 个不超。而且 `__patcheck` 的错误文本**不在
  `SkipClassError` 里**，所以这个差异报成 class 分歧。已写进已知边界清单。

#### Minor 3 条

- `preludeGuards` 里的 `fmt_was_nan` 分支**不可达**：`preludeNaNCoercion` 最后安装，会先
  对数值 NaN 的 fmt 抬 sentinel，所以 guards 那一层永远见不到；分支和建立在它上面的
  `fmt_has_nan_literal` 豁免一起删了。
- 已知边界清单漏了「**函数 / 表形式的 `gsub` 替换**」（结果在 `gsub` 内部产生，包装层看
  不见）。
- 也漏了「**大写** coerced 文本经被拦函数」（needle 出于对称性只能是小写）。

### 17. 第 7 到第 26 轮：按失败模式看，不按轮次看

第 6 轮之后又做了二十轮独立盲审，做法始终一样（新的隔离快照 clone、`bwrap --unshare-net`
断网实测验证、新的无继承上下文 reviewer、完整范围）。逐轮罗列没有意义，因为二十轮里绝大
多数缺陷落在**两处代码**上，而这两处每次都是同一件事：抽象选错了，于是每修一版就换个位置
再犯一次。

先给一个总数，它本身就是结论：**26 轮里有 13 轮抓到的缺陷是我修上一轮时引入的。**

#### 17.1 span recorder 的吸收逻辑：六个版本，每版失败原因不同

span recorder 要把 NaN token 周围的符号列与 printf padding 一起吸收进 span，否则符号差异会
落到 span 之外变成硬失败。这段逻辑改了六版：

| 版本 | 写法 | 失败原因 |
|---|---|---|
| ① | 逐字节 walk，无上限 | wangshu 每字节付一次解释执行，PUC 是 C 循环 —— **1m45s vs 384ms**，单侧 skip |
| ② | 给 walk 加 offset 上限（从 tokStart 数） | 两侧输出长度差一个符号字节 → 所有绝对 offset 差 1 → 停止位置错开一字节 → span 锚点不同，**已知符号差异变成硬失败** |
| ③ | 改从 token 自身位置数 | 同一个问题：那个 offset 本身也是偏移过的 |
| ④ | 一次无锚定 match `" *[%-%+]*[Nn][Aa][Nn] *"` | 前导量词在每个起始位置重试，代价是运行长度的**平方**，约 **1024 字节**就撞 wangshu 的 `maxMatchSteps`；PUC 的 C matcher 没有这个预算，脚本用 pcall 包住时甚至是一次**假分歧** |
| ⑤ | 换成 `$` 锚定的 `" *[%-%+]*$"` | 同一个平方问题换了位置 |
| ⑥ | 不含量词的字符类查找定位「不在 run 内的那个字节」+ 把 gap 反转后按前缀一次匹配 | 收敛：**无重试、无迭代、无 offset 运算** |

后缀方向踩的是同一串坑，只是滞后一轮被发现：前缀那半修好之后，尾部 padding 还在逐字节
走，**230k 个尾部空格让 wangshu 花 3m37s、PUC 花 352ms**，而同样长度的 run 放在 token
**之前**只要 616ms。最终尾部改成在固定长度切片上跑一次 `[^ ]` 查找（切片长度是 harness
常量，两侧扫一样多）。

值得记的是⑥与前五版的区别不在「更聪明」，而在**先把需要的性质列成约束**：无重试（否则撞
matcher 预算）、无迭代（否则一侧付解释执行成本）、无 offset 运算（否则一个符号字节让两侧
错开）。列完再找同时满足三条的写法，一次就收敛。前五版都是针对刚暴露的那**一个**问题打
补丁，所以每次都能修好那一个、又落进另外两条里。

#### 17.2 「限制扫描规模」：五次都栽在同一件事上 —— 比较了一个两侧不相等的长度

扫描要有个规模上限，否则大输入会把一侧推过预算。上限怎么定，试了五次：

1. **输出长度** —— 每个渲染出的 NaN 在一侧多一个符号字节，阈值窗口的宽度就等于 NaN 渲染
   次数，落在窗口里的输入单侧 skip；
2. **输入长度** —— 看起来两侧字节相同，但 `..` 强制转换出来的 NaN 文本可以进到被测量的
   那个串里，符号字节于是也进了被测量的长度；
3. **分桶长度** —— 只是把窗口挪到桶边界；
4. **放宽容差的长度** —— 同样只是把窗口挪宽，窗口还在；
5. **带内容例外的输入长度**（「串里含 nan 字节就走另一条」）—— 内容检查本身要读两侧不同的
   内容，等于把不对称从长度搬到内容。

最后的结论不是第六种比较方式，而是**根本不比较长度**：把扫描本身做成不需要上限的。plain
find 是逐字节搜索，两侧都没有 matcher 步数预算；确实需要 pattern 的地方只在**固定长度的
切片**上跑。八处扫描点全部按这个方式改写。

这一串失败里最尖锐的一条实证是：`#` 和 `==` 能把那个符号字节搬进一个**数字**。
`local a=("y"):rep(N + #(""..(0/0)))` 两侧长度必然差 1，而输出里连一个 nan 字节都没有 ——
任何基于内容的例外都看不见它。这就是「没有哪个长度是两侧相等的」这句话的证明，而不只是
「暂时没找到」。守护它的是 `TestFuzzOracleDiff_NaNGateNoLengthStraddle`，它在 1 次与 50 次
NaN 渲染两档上扫遍阈值窗口，并显式包含把符号字节搬进长度与搬进数字这两类写法。

#### 17.3 四个不对称维度，每维一个专门的守护测试

前六轮只知道两维（值、运行成本）。这二十轮补出另外两维，现在 gate 测试共 **9 个**：

- **运行成本**：harness 的检查在 wangshu 侧是解释执行的 Lua、在 PUC 侧是 C，所以开销随输入
  规模增长的检查会先把一侧推到撞 step budget。载体
  `TestFuzzOracleDiff_NaNGateNoCostAsymmetry`，断的是**成本比值**。
- **分配量**：PUC 的 GC 会在 shim 的 alloc 帽内回收临时串，wangshu 的 arena 回收不了，于是
  同样的 walk 会让 wangshu 先撞 `MaxArenaBytes`。实证是 straddle walk 每个转换复制一次
  fmt 前缀，一段 **103 字节**的脚本分配 O(转换个数 × fmt 长度)，wangshu 耗尽 arena 而 PUC
  在 **706ms** 内跑完。成本比值测试看不到它（它量的是时间，且耗尽的那侧反而早早停下），
  枚举测试也没有这种写法。载体 `TestFuzzOracleDiff_NaNGateNoAllocAsymmetry`。
- **matcher 步数预算**：只有 wangshu 给 pattern 重试计步（`maxMatchSteps`），PUC 的 C
  matcher 完全没有预算。观察到的悬崖在 **1 MiB** 附近。这一维尤其需要单独立测试，因为
  预算触发时失败的那一侧**更快**（提前放弃），成本比值反而缩小；而且脚本用 pcall 包住时，
  一侧抓到错误打印出不同内容，这是**假分歧**而不只是丢一个输入。载体
  `TestFuzzOracleDiff_NaNGateNoMatcherBudgetAsymmetry`。
- **长度阈值**：见 17.2。载体 `TestFuzzOracleDiff_NaNGateNoLengthStraddle`。

`TestFuzzOracleDiff_NaNGateNoAsymmetricSkip` 的枚举也从 47 条长到 **69 条**，补的维度是
fmt 写法、表写法（含把 NaN 放在两个 `#t` 取值**之间**的方向 —— 之前所有表用例都把 NaN 放在
空洞之后，恰好是不翻转结果的那一侧）、以及紧贴 NaN token 的符号 / padding run 长度（几个
尺寸专门跨过 1024 字节那道 matcher 悬崖和旧的 4096 上限）。

#### 17.4 `table.concat` 的两次设计变更

第 5 轮把预扫的上界从 `#t` 换成「检查返回值」之后，还有两处要改：

- **判定入口从「tconcat 是否失败」上摘下来**：只在 `tconcat` 成功返回时检查结果，等于把
  跳过决策挂在一个引擎相关的条件上（带空洞的表在一侧走到 nil 报错、另一侧正常返回）。
  改成**无条件先扫元素**（自 1 起，忽略脚本给的 `i`/`j`，有循环次数上限），再检查返回值
  兜住扫描范围外的元素。
- **上限不能改变判定**：`__concat_scan_cap` 早先在上限处抬 sentinel，后果是**所有**超过
  4096 元素的表整类不可比 —— concat 的边界处理、错误文本比对全丢掉了。改成上限只限制
  **循环次数**，不改变判定。这一条和 17.2 是同一个道理的另一面：规模上限是 harness 自己的
  实现细节，不该变成「这个输入可不可比」的判据。

#### 17.5 测试自身多次「看不到它命名保护的东西」

这一类在第 20、21 轮各出一次，都发生在成本测试上：

- 第一次是**断言选错了量**：只断「两侧 verdict 一致」，把 bug 改回去照样通过 —— wangshu 慢
  **74 倍**但仍在预算内跑完，于是 verdict 相同。改成断成本比值之后反向验证报
  **645 倍 / 上限 120 倍**并失败。
- 第二次是**输入根本没到达被测代码**：为了防住 matcher 预算这一维加的输入上限，让 12 个
  大变体里的 **9 个**在扫描启动之前就短路了，成本测试于是在量一段没跑起来的代码。修法是把
  用例尺寸压到输入上限以内，让它们真的进到扫描（`6574ea0`）。

第二次这个形式比第一次更隐蔽：断言是对的、量也是对的，失效的是「用例有没有走到被测路径」
—— 也就是 [[prove-the-path-under-test]] 的原始命题，只不过这次载体是 harness 的守护测试。

#### 17.6 产品侧的一处改动

`internal/stdlib/stringlib.go` 里 `string.find` 的 plain 路径原来写
`strings.Index(string(s[init:]), string(pat))`，每次调用把剩余 subject 复制成一个新 string，
于是「在一个长 subject 上循环 find」是平方的。harness 正是这样扫格式串的，表现为**输入长
30 倍、成本长 205 倍**。改用 `bytes.Index` 消掉复制。这是纯性能修复、语义不变，但它说明一件
事：harness 的成本对称性有时会牵到产品代码的复杂度上，而那处复杂度在产品自己的 benchmark
里从来没暴露过。

## 教训

### 教训 1（头条）：连续多次同类缺陷是「机制选错」的信号，不是「再仔细一点」的信号

同一个机制（输出端的 `__emit_nan_gate`）上，四轮审计抓出四个缺陷，全是同一类：判据的
某个输入是两侧不同的量。四次的载体分别是本引擎拼写作 needle、对称标记作用于不对称数据、
按值索引的 provenance key、只剥第 1 字节的 key。**每一次修完我都自认已经想清楚了**，
下一轮又在同一个机制上换个位置犯同一件事。

（机制删掉之后这一类**并没有停**：第五轮在**入口拦截**这一侧又出了第五个实例（`#t` 作
预扫边界），第六轮出了第六个（判据的**运行成本**两侧不同）。所以这条教训不是「删掉那个
机制就结束了」，而是「这类缺陷会跟着换载体」—— 区别在于第五轮起我开始用这条教训，而不是
只在事后引用它，见下面的正面案例。）

前三轮我做的都是同一件事：**把 key 修得更聪明**。第四轮我才去问另一个问题：**这个 key
能不能对称**。答案只需要一步推导：要保住 #173 的 span 分类，这个检查必须豁免浮点转换
结果；唯一可用的豁免判据是「这个字符串有没有 provenance」；Lua 5.1 没有 string identity
primitive，所以只能按字符串的**值**查表；而那个值恰恰就是两侧的差异。于是任何一个恰好
等于某侧拼写的脚本字面量，只会在那一侧命中豁免 —— **这个机制在这个约束下不可能做对**。

**这一步本该在第二轮就做。** 它比第二、三、四轮的任何一次修补都便宜（一次推导，没有
代码），而且它一次就把后面两轮的缺陷全部消掉了。

判据：**如果同一个机制上反复出现同一类缺陷，停止修补，去证明「这个机制在这个约束下能
不能做对」。** 触发条件建议放在**第二次**，不要等第四次。具体做法是把机制的约束写成
一条链，看链上有没有一环必须读取被测差异本身：

1. 这个机制为了不破坏别的东西，必须**豁免**哪一类输入？
2. 判断「是否豁免」的唯一可用信息是什么？（列出宿主语言真的提供的原语，不是想要的）
3. 那个信息是不是经过了被测差异？如果是，机制不可能对称，**到此为止**。

**「每次修完都自认想清楚了」这个感觉本身就是信号。** 它说明我在验证的是「我写的这条
规则」，而不是「这个机制的存在前提」。修补只能改规则，改不了前提。

#### 第五轮：这条教训第一次真的被用上，而且立刻见效

前四轮这条教训只是**事后总结**：第四轮我是在第四个缺陷之后才去问机制的前提。第五轮才是
它**第一次在缺陷刚出现时就被拿来用**，正面案例如下。

第五轮的 blocking 是 `table.concat` 预扫把范围钳到 `#t`，而带空洞的表 `#t` 在 Lua 5.1 里
未规定、两侧引擎取值不同（第 15 节）。按前四轮的惯性，下一步会是「把钳范围的算法修得更
聪明」—— 比如改用 `next` 遍历、或者取 `#t` 与某个上界的并集，再多加几个特殊情况。

这次改成问**这个预扫的前提对不对**，一步就有答案：预扫要复制 `tconcat` 自己的遍历，就
必须**猜它的边界**；而默认边界是 `#t`，`#t` 在带空洞的表上无法两侧一致地猜。前提不成立，
所以任何「更聪明的边界算法」都是白做。

**结果是方案反而变简单了**：改成检查 `tconcat` 的**结果字符串** —— 它实际读了什么全在
返回值里，**根本不需要边界**，判据退化成与别处一样的那个固定小写 needle。代码更短、判据
更少、并且和 `__gate1` 复用同一个判据。

这个对照值得单独记下来：**问前提通常不是「更贵的路」，而是更便宜的路。** 修算法要为每个
新情况加一个分支，问前提往往一步就把整类情况消掉。前四轮我把「问前提」当成一件重活留到
最后，第五轮证明它是最省的那一步。所以触发条件放在**第二次**是合理的，甚至可以更早 ——
凡是发现自己在给判据加边界猜测、加特殊情况、加归一化步骤时，就该先问一次前提。

这条与下面的教训 2 是层级关系：**教训 2 是机制内部的正确性判据**（判据的每个输入都必须
两侧相同），**这一条是要不要保留这个机制的判据**。先过这一条，再谈教训 2 —— 如果机制
本身不可能对称，那么把判据修到多精确都没有意义。第四轮的最终结果是删掉机制、把那类输入
改报**对称的差异**，同时拿回了一片被误跳的覆盖面（`print("banana")` 等），也就是说
「删掉」不是纯退让，是净收益。

### 教训 2：差分 harness 判据的**每一个输入**都必须两侧相同 —— 包括它读的值、它查表用的键，以及**它自身的执行成本**

第一轮我把这条写成「判据必须由两侧完全相同的数据推导，不能由本侧的观测值推导」。
方向对，但**不够精确**，第二轮直接证明了它的漏洞；第三轮又证明：把表述改精确、并写下
「要端到端检查每个输入」之后，同一类错误**还是**会换一个位置再犯一次。

第一轮的错误形式是显式的：needle 用 `tostring(0/0)`，一个每侧不同的值，于是 PUC 侧
找 `-nan`、wangshu 侧找 `nan`，`print("banana")` 只在一侧被拦。第二轮的错误形式更
隐蔽：我加的 `__fmt_nan_rendered` 标记**确实**只由 fmt 字符串 + 参数类型算出，两侧
一致，按第一轮那条教训的字面表述是合格的；但标记只是**开关**，它打开的那个扫描读的
是运行时字符串**内容**，而内容两侧不同（`NAN` vs `-NAN`），所以整体判据仍然分叉，
偏移差 1 就翻转方向。

**一个「对称的规则」作用在「不对称的数据」上，整体依然不对称。** 所以自检要问的不是
「我的规则对称吗」，而是「**这个判据表达式端到端求值，两侧结果一样吗**」—— 把被读的
字符串内容、被读的长度、被读的字节，全都算进输入里。

判据 / 自检清单：写差分 harness 的每一个跳过 / 判定判据时，把它当一个表达式整体求值，
逐个列出它读到的量，问每一个量两侧是否相同。`tostring(NaN)`、`#coerced`、字符串的
内容和样子、词边界，凡是经过被测差异本身的量都不合格 —— **哪怕决定「是否去读它」的
那个开关是对称的**。合格的只有引擎无关的固定字面量，以及完全不接触被测差异的运行级
标记。在「任一侧 limit 即 skip」的框架下，判据分叉的后果是单侧 skip，**静默吞掉同一
次运行里的真实差异**（第一轮实证：`print("banana") print(string.len(""..(0/0))*100)`
把活的 400-vs-300 差异一起带走）。这比误报严重得多 —— 误报会亮红灯，单侧 skip 什么
都不说。

清单还要加一问，**它与值的对称性无关、必须单独过**：这个判据**自身的开销**，两侧付得起
吗？具体是三步 —— ① 这个检查的复杂度随输入的哪个维度增长（本轮是 fmt 的**长度**）；
② 那个维度在 fuzz 里能被推到多大（80 KiB 的 fmt 是 fuzz 轻易构造得出的）；③ 在开销更大
的那一侧，把维度推到那么大会不会撞预算。合格的检查要么复杂度只跟着「真正相关的东西」走
（本轮改成跟着**转换个数**走，而不是 fmt 长度），要么**只在确实可能有东西被拦时才启动**
（本轮的启动判据是每参数 O(1)、且由**参数类型**推导，所以两侧一致）。这两条修法合起来把
38s 降到 264ms，与 base 的 216ms 同一档。

**这一轮同一类错误我犯了六次，每次载体都不一样**：

1. **needle 用本引擎拼写**（第一轮 blocking）—— 判据直接读了一个两侧不同的值；
2. **对称标记 + 不对称的扫描对象**（第二轮 blocking）—— 开关对称，被读的字符串内容
   不对称；
3. **provenance 的 key 里藏着本引擎拼写**（第三轮 blocking）—— 判据本身没问题，但它
   查表用的那个 key 是两侧不同的量，于是两侧查的其实不是同一格；
4. **剥符号的 key 只剥第 1 字节**（第四轮 blocking）—— key 归一化只在 nan token 紧贴
   串首时成立，fmt 带字面前缀或宽度 padding 时符号落到串中间，两侧 key 又分叉；
5. **`#t` 作预扫的边界**（第五轮 blocking）—— 带空洞的表 `#t` 在 Lua 5.1 里未规定，
   两侧引擎取值不同，于是「NaN 在不在扫描范围内」变成引擎相关；
6. **判据自身的执行成本两侧不同**（第六轮 blocking）—— 判据读的值全部对称，**开销**不
   对称：逐字节扫 80 KiB 的 fmt，PUC 侧 392ms 判 OK，wangshu 侧 1m22s 之后撞预算判
   `VerdictLimit`。

另有一次同族但属教训 3 那一面的失效：pattern 引擎假绿（判据被环境改写）。

**⑥ 是这条教训需要扩维的原因。** 前五个都是「判据读的**值**两侧不同」，⑥ 里没有任何值
两侧不同 —— 一个 80 KiB 的、不含 NaN 也不含任何转换的 fmt，两侧读到的字节完全一样，判据
最终算出的结论也一样。分叉的是**算这个结论要花多少**。在这个 harness 里每个检查在 wangshu
侧是**解释执行的 Lua**、在 PUC 侧是 **C**，所以一个开销随输入规模增长的检查，必然先把一侧
推到撞预算，而另一侧还在轻松跑完。后果与前五个完全一样：单侧 skip，静默吞掉同一次运行里的
真差异 —— 只是经由**时钟**而不是经由值。

所以这条教训的正确表述是：**判据的输入包括它读的值、它查表用的键，以及它自身的执行成本。**
在两侧语言实现不同的 harness 里（一侧解释执行、一侧 C），任何随输入规模增长的检查都必须
额外问一句「**另一侧付得起吗**」。这个问题和「值对称吗」是**独立的**两问，一个都不能省。

**第 7 到第 26 轮把「另一侧付得起吗」这一问拆成了三个互不重叠的资源**（第 17.3 节），每个
都得单独过，也各配了一个守护测试：

- **时间** —— 一侧解释执行 Lua、一侧 C，开销随输入规模增长的检查会先把一侧推到撞 step
  budget。`TestFuzzOracleDiff_NaNGateNoCostAsymmetry`，断成本比值。
- **分配量** —— PUC 的 GC 在 alloc 帽内回收临时串，wangshu 的 arena 回收不了，所以同样的
  walk 会让 wangshu 先撞 `MaxArenaBytes`。实证：straddle walk 每个转换复制一次 fmt 前缀，
  103 字节的脚本就耗尽 wangshu 的 arena，而 PUC 在 706ms 内跑完。
  `TestFuzzOracleDiff_NaNGateNoAllocAsymmetry`，断 verdict 一致（arena 耗尽正好破坏它）。
- **matcher 步数预算** —— 只有 wangshu 给 pattern 重试计步，PUC 的 C matcher 没有预算，
  悬崖在 1 MiB 附近。这一维**成本比值测不出来**：预算触发时失败的那一侧反而更快（提前
  放弃），比值缩小而不是增大；而且脚本用 pcall 包住时它是**假分歧**不只是丢输入。
  `TestFuzzOracleDiff_NaNGateNoMatcherBudgetAsymmetry`，断长度这个两侧一致的量。

三者的共同点是「判据自身消耗的资源」，区别在于哪一侧先耗尽、以及耗尽时的表现（变慢 / 报
内存 / 提前放弃）。**「成本对称」不是一个问题，是三个。**

还有第四维在值与资源之外：**长度阈值本身**（第 17.2 节）。任何长度都可能两侧不等，因为
`..` 强制转换出来的 NaN 文本带一个符号字节，而 `#` 与 `==` 能把那个字节搬进一个数字。这一维
的结论不是「换一个更好的长度」，是**不比较长度**，见教训 12。

③ ④ ⑤ 尤其说明问题：它们都发生在我**已经把前面几个写成教训、并在教训里明确写下「要端到端
检查每个输入」之后**。第二轮那次是在我自己写的禁止性注释下面几行违反的，第三轮换了个
藏法，第四轮则是我上一轮的修复本身只覆盖了一部分情况，第五轮**又是我修上一条时引入的**
（第三轮为消副作用而加的 `#t` 钳范围）。

**这类错误的顽固性来自「判据的输入」这个概念本身容易被想窄。** 六次我都认真检查了**我
写的那条规则**，漏掉的分别是：规则**读的那个值**（①）、武装规则的那个**开关所作用的
对象**（②）、**存放证据的那个键**（③）、归一化那个键时**只处理了一部分位置**（④）、
规则**遍历时猜的那个边界**（⑤）、以及规则**自身花掉的时间**（⑥）。每次漏的都不是规则，
而是规则周边那些「看起来只是实现细节」的量。只要还依赖「我这次想清楚了」这种感觉，下一次
就会换一个我还没想到的位置再犯 —— ⑥ 更进一步说明，下一次那个位置可能连「量」都不像，
它是资源而不是数据。

所以正确的沉淀**不是记规则**，而是**把 gate 路径上所有读取点列出来逐个过**（`grep`
出每一处读取，逐个问「这个量两侧相同吗」），并且**把这个枚举固化成测试**。第三轮我照
这个做法写了 `TestFuzzOracleDiff_NaNGateNoAsymmetricSkip`（根包，29 种写法，断言任何
输入不得只在一侧 skip），也做了反向验证（把 provenance key 改回按值索引，报
`7/29 shapes decide asymmetrically`）。但第四轮证明**这份枚举的维度选错了**：29 条里
`string.format` 的 fmt 全是裸 verb，恰好是当时那个 key 唯一能正确归一化的写法，加一个
字面前缀立刻报不对称。第五轮补上 fmt 写法 / 表写法 / 参数位置三个维度（29 → 47 条），
第六轮又发现**成本这个维度根本不在枚举里**，得单独立一个测试
（`TestFuzzOracleDiff_NaNGateNoCostAsymmetry`）—— 因为它断言的量不是 verdict 而是**比值**，
见教训 8 的补强。

**前四轮的结论是更上一层的**：四次都是同一个不可对称的 key 的变体，所以正确的动作不是把
枚举做得更全，而是先按教训 1 证明这个机制能不能做对。第四轮证明它不能，机制已删除。第五、
第六轮说明这条教训**并不因此结束** —— 同一类缺陷换到入口拦截这一侧继续出现（⑤ ⑥），只是
第五轮起我开始在缺陷出现时就用教训 1，而不是等到第四次。

这条和下面的教训 3 是同一个家族的两个面：教训 3 是「判据被环境改写而失效」，这条是
「判据被差异本身污染而分叉」。再加上教训 10（判据有副作用），三条都属
[[prove-the-path-under-test]] 家族。

### 教训 3：在被包装过的环境里做检查，不能依赖会被那层包装改写的原语

门层跑在 `preludeGuards` 之后，用了已被 `__patcheck` 包装的 `string.find`。
包装的量词上限把门自己的 pattern 判掉，两侧**对称**报错 → 两侧输出都是空串 →
`CompareOutput` 判 `equal`。检查静默失效，而测试显示「相等」，是假绿。

判据：**门 / 检查逻辑必须只依赖不会被它所保护的那层改写的原语**。要么手写实现
（本轮的逐字节 verb 扫描），要么在包装之前捕获原语并以私有名字传进来（本轮的
`__oracle_sfindraw`），且用完立刻置 nil 不暴露给被测脚本。

推论：对称的失败模式最危险。两侧同样地坏掉 = 输出相同 = 比较器说相等。凡是
「两侧共享同一层 harness 代码」的差分测试，这层代码自身出错的默认表现就是假绿，
不是红灯。

家族归属：与 [[prove-the-path-under-test]]、以及
[[2026-07-25-issue179-test-go-fuzz-retry-revive-round]] 教训 1（rc + 反向 grep
假绿，positive marker 必需）同族 —— 都是「判据集恰好被短路满足」。#179 那轮说
shell 层，本轮说 prelude 分层内部。这是该家族的**第 17 个实例，且是第 2 个
「非 Go 测试 harness 载体」**。

### 教训 4：已知平台差异的定性有适用边界，收窄比扩展更常见

#173 定性时的依据「IEEE 754 不赋 NaN 符号数值语义」对**符号位本身**成立，但符号
字符一旦进入字符串就改变了长度和内容，衍生出的 `string.len` 返回值（4 vs 3）是
有语义的数值，`string.len(0%0)*100` 更是把它放大成 400 vs 300 这种完全不含 NaN
token 的纯数值差异。

判据：给一类差异做「已知 / 可忽略」定性时，要问「**这个差异能否经由某个操作转化
成有语义的值**」；能转化就说明定性的边界比看上去窄，必须在定性文本里写清适用的
出口范围（本轮：浮点转换成立，文本 coercion 不成立）。

家族归属：这是 [[2026-07-24-issue173-oracle-nan-known-diff-round]] 教训 1
（定性 + 证据链 + 窄口径归类三件套）的**边界补充** —— 三件套本身没错，但
「定性」这一步要额外加一次可转化性检查。跨 2 实例（#173 定性 + 本轮收窄），
建议下次再遇到「已知差异定性需要修边界」时把这条并入那条三件套作为第四件。

### 教训 5：修 fuzz 撞到的写法之前，先把同族出口面整个测一遍

fuzz 撞到 2 种写法；探针测出 7 种无证据 + 5 种数值泄漏。只修撞到的两种就是
benchmark-shaped 窄修复 —— 修完 nightly 还会在同一个根因上继续撞新 corpus，
而且每次都长得像「一个新的边界情况」。

判据：拿到一个 crasher，先定位「产生这个差异的机制」，再枚举**该机制的所有出口**
（本轮是 Lua 里所有把 number 转成 string 的路径），逐个探针实测，然后才决定修法
的粒度。这条与仓库既有纪律「不做 benchmark 形状的窄修复」一致，本轮是它在 fuzz
crasher 处理上的一个实例。

### 教训 6：宿主语言的能力缺失会强制方案分两套手法

`..` 在 Lua 5.1 层面无法拦截：读 PUC 源码 `lvm.c` 的 `luaV_concat` 核实它对
`ttisnumber` 直接走 `tostring` 宏、不触发元方法，wangshu 侧探针双向实测确认绕过
全局 `tostring`，且 `debug` 不在白名单所以 `debug.setmetatable` 这条路也没有。
于是「拦源头」的统一方案做不到，必须配一套「输出端检测」兜底，而两套的误伤面
不同：源头拦精确；输出端检测会误伤含该拼写的脚本字面量，代价是丢输入而不是错判。

判据：设计拦截类机制时，先确认宿主语言在**所有**入口都给了 hook；缺一个就要准备
第二套手法，并明确写下它的误伤面与误伤后果（丢覆盖 vs 判错）。选「丢覆盖」这一侧
才安全 —— 本轮输出端检测用大小写敏感 plain 查找，把 `print("BANANA")` 这类负例
留在可比一侧。

审计之后这条有了更完整的样本：不只 `..` 缺 hook，`#`（OP_LEN）对 string 直接读
`tsvalue(rb)->len` 也不触发元方法，两个缺口串起来（`#(""..(0/0))`）就形成 prelude
完全观察不到的路径。缺口不止会逼出第二套手法，还可能连第二套也覆盖不到，这时唯一
诚实的做法是把它写成公开的已知边界并用测试固化。

第二轮又给这条加了一层：第二套手法的**覆盖上限不是由宿主语言决定的，而是由判据对称性
决定的**。输出端检测想再往前一步（覆盖大写跨 concat straddle）就必须读 coerced 字符串
的内容，而内容两侧不同，于是必然单侧命中。所以「第二套手法覆盖不到」有时候不是「还没
想到办法」，而是「任何办法都会破坏对称性」—— 这时候写成公开的已知边界不是退让，是唯一
正确的做法。

### 教训 7：「我自查过」不等于「被审过」，而且「审完修完」也不等于「审过」

我自查时跑了双侧探针、做了反向验证、写了 20+13 组测试，仍然漏掉 8 条，其中 3 条
blocking。漏的原因有共性：我验证的是**我想到的那些写法**（大写 `BANANA`，因为文档
里提过），没有系统枚举「同一判据下的其他输入类」（小写的普通英文词）。独立盲审的
价值恰在于**它不知道我想到了什么** —— 它按范围重新枚举，于是一次就撞出 `banana`。

第二轮把这条推进了一步：**修复本身也要过审**。范围只看第一轮那三个修复 commit 的
增量盲审又找出 1 blocking / 2 important / 3 minor，全部成立，其中 blocking 是修复
**引入的新缺陷**。修复代码往往写在「已经想清楚了」的心态下，而它恰恰是整轮里最新、
被验证最少的一段。

判据：涉及「整类不可比」「整类跳过」这种**范围性断言**时，自查必然带着写代码时的
思维定势，必须过独立审计。审计要给的是仓库规则、任务目标、固定范围和它自己跑出来
的原始证据；**不能给**父对话、预期结论和我心里的可疑点，否则它只会复述我的盲区。
而且审计修完之后要再跑一次**范围收窄到修复 commit 的增量盲审**，别把「审过一遍」
当成整轮都被审过。

### 教训 8：「新加的测试 PASS」不等于「该性质成立」

`TestFuzzOracleDiff_NaNGateSymmetry` 断言的正是「任何脚本不得一侧 limit 一侧正常」，
也就是第二轮 blocking 违反的那条性质，而它在含缺陷的代码上是 PASS 的 —— 只因为用例
表里没有 `(...):sub(1,4)` 那个形式。测试名字写着全称量词，实际强度只有表里那几行。

判据：写「任何 X 都不得 Y」这种**全称断言**的测试时，**用例表的覆盖面就是断言的实际
强度**，函数名和注释里的「任何」不产生任何约束力。所以要**反向验证**：故意构造一个
按断言应当触发失败的输入，确认测试真的会红。做不到就说明测试只是在陈述意图，不是在
检查性质。这与教训 3 的「假绿」是一对：教训 3 是判据被短路满足，这条是判据的**定义域
太小**，两者的表现都是绿灯。

**第四轮的补强：把 hand-picked 换成「机械枚举」并不自动解决这个问题 —— 枚举测试的强度
取决于「枚举了哪个维度」，选错维度的枚举和 hand-picked 一样弱。**

第三轮我写了 29 条枚举测试，自认为已经把这条教训做成可执行的东西了。第四轮 blocking
证明它照样漏：那 29 条里 `string.format` 的 fmt **全是裸 `%e` / `%E` / `%f` / `%g`**，
恰好是当时那个 `__prov_key` 唯一能正确归一化的写法。加一个字面前缀（`"x%e"`）或宽度
padding（`"%5e"`）就立刻报不对称。我枚举的是「provenance 的**消费时序**」（登记未消费、
乱序消费、同值产生两次、进 pcall / 表构造器 / 条件判断），而缺陷是沿「fmt 的**写法**」
这个维度变化的 —— 我枚举了一个维度，缺陷藏在另一个维度。

判据补充两条：

- **写枚举测试之前先定维度**：问「这个缺陷可能沿哪几个维度变化」，把维度列出来，再对
  每个维度取几个代表值，而不是沿着自己最熟的那个维度铺 29 条。本轮真正相关的维度至少
  有两个：provenance 的消费时序（我枚举了）、fmt 的写法（我漏了）。名义上的条数多不
  等于维度覆盖全。
- **反向验证要覆盖已知缺陷的每一种，不是随手一种**：我做了反向验证，但只验了「旧的按值
  key」那一种（报 `7/29 shapes decide asymmetrically`），没有验「前缀 fmt」那一种。
  反向验证只能证明测试对**我构造的那个**缺陷有效；每一个已经知道的缺陷变体都要单独反向
  验证一次，否则「反向验证过了」给的信心是虚的。

**第五轮的补强：补维度的效果可以被量化，而且量化结果说明维度比条数重要得多。**

第五轮审计指出那 29 条还有第二个盲区：**表也全是无空洞的**，`string.format` 的 fmt 也仍
全是裸 verb。补上三个维度之后变成 47 条：

- **fmt 写法**：字面前缀 / 宽度 / padding / 多个转换；
- **表写法**：空洞 / 间隙 / 稀疏；
- **参数位置**：`gsub` 的 replacement / `%s` 与 `%q` 槽位。

反向验证给出的对比很直接：把 `#t` 预扫改回去，47 条报 `3/47 shapes decide
asymmetrically`，而**旧的 29 条一条都报不出来**。同一个缺陷，一份枚举抓得到、另一份完全
看不见，差别只在维度，不在条数。

**第六轮的补强（两条，都是新东西）：**

**第一条 —— 成本这个维度根本不在枚举里。** 前面所有维度都是「输入长什么样」，第六轮的
缺陷沿的是「输入有多大」，而且后果不经由值、经由时钟。这个维度不可能靠往 47 条里再加几条
覆盖，因为它要断言的量根本不是 verdict，得单独立一个测试。

**第二条 —— 断言选错了量，反向验证照样通不出来。** 新增的
`TestFuzzOracleDiff_NaNGateNoCostAsymmetry` 第一版只断言「两侧 verdict 一致」，把 bug 改
回去它**照样通过**：wangshu 侧慢了 74 倍，但仍在预算内跑完，于是两侧 verdict 相同。必须
断言的是**成本比值**，因为那才是「输入再大一点就变成单侧 skip」的那个量 —— verdict 只是
比值越过阈值之后的离散后果，用它做断言等于要求测试输入刚好落在阈值另一侧。改成断比值之
后，反向验证报 **645 倍 / 上限 120 倍**并失败。（写法上：每个形式与**自己的短输入版本**
比，让共享机器负载同时影响比值两端，不影响结论。）

所以这条要加一句**可操作的**判据，它比「反向验证过了」严格得多：

> **写完枚举测试之后，对每一个已知的历史缺陷做一次反向验证 —— 每个都验，不是只验一种。**

理由是本轮两次踩到同一件事：第四轮那次只验了「按值 key」一种，漏掉「前缀 fmt」；第六轮
那次只验了「verdict 是否相同」一种断言，漏掉「成本比值」。**只验一种时，通过的信息量几乎
是零** —— 它只说明测试对我刚才手里那个缺陷有效，而那个缺陷我本来就知道。逐个历史缺陷验
一遍才能发现「这份测试有一整类缺陷看不见」，而这正是第五轮 `3/47` vs `0/29` 那个对比、
以及第六轮「断 verdict 通过 / 断比值失败」那个对比暴露出来的东西。

而最上层的结论仍归教训 1：当同一机制上反复出现同类缺陷时，正确的动作不是把枚举做得更全，
是去证明这个机制能不能做对（第四轮），或者去问这个判据的前提对不对（第五轮）。

### 教训 9：修复引入回归的概率与修复的巧妙程度成正比

第一轮的 8 条修复里，最「聪明」的那一步 —— 用运行级标记武装一个更宽的扫描 —— 就是
引入回归的那一步。朴素的那几步（needle 换固定字面量、补上漏掉的 pattern 函数、
`t[k]` 改 `rawget`、`tostring` 保持 varargs）全都没问题。

原因不难理解：朴素修复是在已有的正确规则里补齐遗漏，巧妙修复是在**引入新机制**，
而新机制自带一套自己的前提，那些前提没有经过原规则同样的推敲。第二轮那个标记的前提
是「标记对称就够了」，这个前提从来没被检验过。

判据：修复时如果发现自己在**为某个边角情况设计额外机制**（新标记、新状态、新的更宽
判据），把那个机制单独拎出来当成一次新的设计对待 —— 它是整份修复里最需要被独立审计
的部分，也是最该优先考虑「不修、写成已知边界」的部分。本轮最终就是这么做的：删掉
机制，边角情况改报对称差异。

### 教训 10：自己加的检查可能把一条既有的真差异藏起来

第三轮的 important：`table.concat` 的预扫跑在真 `tconcat` 校验参数**之前**。非数值的
`i` / `j` 会先触发预扫里的数值 `for`，把 `tconcat` 自己的 `bad argument #4` 盖掉 ——
而那条错误消息两侧本来有真差异，于是**一条既有的真差异被我加的检查掩盖成 `equal`**。
同一处还有第二个面：`j` 给 `1e15` 会让预扫真的循环起来，两侧各烧数百毫秒撞预算判 skip，
而 base 上 1ms 就报出了那条真差异。

判据：给差分 harness 加**任何**前置检查时，除了问「这个检查对称吗」（教训 2），还要问
「**这个检查会不会改变原本会发生的事**」。至少三个面要过一遍：

- **错误发生的顺序** —— 我的检查会不会先报错，把被测实现自己的报错顶掉；
- **错误的文本** —— 顶掉之后两侧变成同一条 harness 消息，比较器就判等了；
- **执行时间** —— 检查引入的额外工作会不会把一个毫秒级就能报出差异的输入拖到撞预算，
  从而变成 skip。

修法方向是把检查**钳进被测实现自己已经接受的参数域**：只在参数已经是 `tconcat` 能接受
的形式时才预扫，范围钳到 `#t`。也就是说，前置检查不能比被它保护的那个函数更严格、更慢、
更早报错。

> 第五轮补记：**这个修法本身成了第五轮的 blocking。**「钳进被测实现接受的参数域」这个
> 方向没错，错在钳的那个边界 `#t` 是两侧不同的量（带空洞的表 `#t` 在 Lua 5.1 里未规定）。
> 更根本的问题是：只要检查还在**复制被测函数自己的遍历**，它就必须猜那个函数的边界，而
> 边界能不能两侧一致地猜是没保证的。第五轮的最终修法绕开了整件事 —— 检查 `tconcat` 的
> **结果**而不是它的输入，结果里天然只包含它实际读到的东西，不需要边界（见第 15 节和
> 教训 1 的正面案例）。所以这条判据要补一句：**前置检查如果需要重现被测函数的遍历，先看
> 能不能改成检查它的输出** —— 检查输出不需要猜任何前提，也顺带消掉副作用那三个面。

这与教训 2 是**不同的失效模式**：教训 2 是判据不对称（两侧算出不同结论），这条是判据
**有副作用**（两侧算出同一个结论，但那个结论本身是被检查改出来的）。两者的表现也不同：
不对称给出单侧 skip，有副作用给出假的 `equal` 或对称 skip。

### 教训 11：同一处代码改到第三版还在出同类缺陷，说明这处的**抽象**选错了，不是实现没写对

这条是教训 1 在**单点代码**这一层的对应物。教训 1 说的是「一个机制反复出同类缺陷就去证明
机制能不能做对」；这一条说的是更细的粒度：**同一个函数、同一段循环，改到第三版还在出同一
类问题时，问题不在这一版写得对不对，在于这段代码被组织成了什么形状。**

本轮最清楚的样本是 span recorder 的吸收逻辑，**六版才收敛**（第 17.1 节）。前五版的共同点
是每一版都**修好了上一版暴露的那一个问题、又引入一个新的**：逐字节 walk 太慢 → 加 offset
上限 → offset 本身两侧差 1 → 换个基准点数 offset → 还是差 1 → 改用一次 pattern match →
量词重试是平方、撞 matcher 预算 → 换个锚定位置 → 还是平方。五次都是针对刚暴露的那一个
现象打补丁。

真正收敛的那一版做法不同：**先把需要的性质当作约束列出来，再找同时满足全部约束的写法。**
本轮的三条约束是

1. **无重试** —— 否则撞 wangshu 的 matcher 步数预算（PUC 没有这个预算）；
2. **无迭代** —— 否则一侧付解释执行成本、另一侧付 C 循环成本；
3. **无 offset 运算** —— 否则两侧那一个符号字节让所有绝对位置差 1。

三条一起看，答案几乎是唯一的：不含量词的字符类查找定位边界，加上把 gap 反转过来按前缀
一次匹配。而单看任何一条，前五版里都有某一版是「满足这一条」的 —— 这就是逐个打补丁一定
会来回震荡的原因：约束不止一条，每次只盯着最新暴露的那一条，改动必然踩回另外几条。

判据：**同一处代码第三次出现同类缺陷时，停下来做一件事 —— 把这段代码必须满足的性质全部
列出来（把前几版各自暴露的那条都算进去），然后问「有没有一种写法同时满足全部这些性质」。**
如果有，直接写那一种，不要在当前版本上继续加分支；如果没有，说明这段职责本身分配错了，
按教训 1 去问它的前提。

配套的一个信号：**如果第 N 版的改动内容是「给上一版加一个上限 / 加一个特殊情况 / 换一个
基准点」，那它几乎肯定是第 N+1 个缺陷的来源。** 收敛的那一版通常反而**更短**（本轮⑥比②
③④⑤都短），因为它不需要那些补丁。

与教训 1 的关系：教训 1 问「这个机制该不该存在」，这一条问「这段代码该长成什么形状」。
触发次数上这一条更早 —— 第三版就该触发，而且成本很低，只是把约束写下来。

### 教训 12：「不比较那个量」往往比「把比较做对」更可行

「限制扫描规模」这件事失败了**五次**（第 17.2 节），五次都是同一个错误：**比较了一个两侧
不相等的长度。** 输出长度 → 输入长度 → 分桶长度 → 放宽容差的长度 → 带内容例外的输入长度。
每一次我都以为找到了「两侧相等的那个长度」，下一轮都被证明不是。

最后的解法不是第六种比较方式，而是**取消比较**：把扫描本身做成不需要上限的（plain find
在两侧都没有 matcher 步数预算；确实需要 pattern 的地方只在**固定长度的切片**上跑）。八处
扫描点全部这样改写之后，harness 里不再有任何长度阈值。

这里面有一个可迁移的判据：**当一个量在两侧不可能相等时，不要试图容忍这个差异 —— 容差、
分桶、例外都只是把出问题的窗口挪个位置，窗口本身还在。要问的是「能不能不读这个量」。**

本轮的「不可能相等」是可以证明的，不是「暂时没找到」：`..` 强制转换出来的 NaN 文本带一个
符号字节，而 `#` 与 `==` 能把那个字节搬进一个**数字**（`("y"):rep(N + #(""..(0/0)))`），
这时输出里连一个 nan 字节都不剩，任何基于内容的例外都看不见它。所以「找一个两侧相等的
长度」这个方向是死的。

判据三步：

1. 先问这个量**能不能**两侧相等 —— 能不能构造出一个输入让它不等？能构造出来就到第 2 步，
   不要先去设计容差；
2. 再问这个比较**是为了什么** —— 本轮是「别让扫描把一侧推过预算」；
3. 最后问那个目的**有没有不读这个量的实现** —— 本轮有：让扫描本身不需要上限。

第 3 步经常是有答案的，而且答案通常更简单（本轮的收益是整类长度阈值消失，同时 gate 不再
有「大输入整类不可比」这种覆盖损失）。这与教训 1 第五轮那个正面案例是一回事的两种说法：
**问「能不能不依赖这个量」通常不是更贵的路，是更便宜的路。**

反面的代价也要记下来：`__concat_scan_cap` 那一版在上限处抬 sentinel，让**所有**超过 4096
元素的表整类不可比，把 concat 的边界处理和错误文本比对全丢掉了（第 17.4 节）。所以规模上限
只能限制 harness 自己的**循环次数**，绝不能变成「这个输入可不可比」的判据 —— 后者等于用一个
实现细节切掉一片覆盖面。

## Promotion 候选

- **教训 1（新头条：反复同类缺陷 = 机制选错，不是不够仔细）**：**直接升进
  [[prove-the-path-under-test]] 正文，作为该 guide 的第一节；如果它在那份 guide 里显得
  层级更高，就单独立一份小 guide（建议名 `stop-patching-prove-the-mechanism`），把
  [[prove-the-path-under-test]] 作为它的下一层。** 理由：
  - **样本强度足够**：同一机制、四轮审计、四个同类缺陷，每一次修完我都自认想清楚了。
    这不是「实例数够不够」的问题，是同一个错误的四次重演。机制删掉之后第五、第六轮又在
    入口拦截那一侧各出一个同类实例（`#t` 作边界、判据成本不对称），说明这一类会跟着换
    载体，光删一个机制不构成结束。
  - **有了正面案例，不只是事后总结**：第五轮的 blocking（`#t` 作预扫边界）出现时我**没有**
    去修钳范围的算法，而是按这条教训问「预扫的前提对不对」，一步就得到答案（复制 `tconcat`
    的遍历必然要猜边界，而默认边界猜不了）；换成检查**结果字符串**之后判据不再需要边界，
    方案反而更简单、并且复用了已有的固定小写 needle。这条与前四轮「一直在修算法」正好构成
    对照，**也纠正了一个隐含假设：问前提不是更贵的路，通常是更便宜的路。** 写进 guide 时
    要把这个对照一起写上，并加一条更早的触发信号 —— **凡是发现自己在给判据加边界猜测、
    加特殊情况、加归一化步骤，就该先问一次前提。**
  - **它是层级更高的判据**：教训 2 是**机制内部**的正确性判据（判据的每个输入都必须两侧
    相同），这一条是**要不要保留这个机制**的判据。机制不可能对称的时候，把判据修到多
    精确都没意义 —— 前三轮的全部工作就是这么白做的。所以顺序上必须先过这一条。
  - **它的成本极低而收益极大**：本轮的证明只用了一步推导（豁免判据必须按值查表，而值就是
    差异），没写一行代码，一次消掉后面两轮的缺陷；而且删掉机制之后还净赚一片覆盖面
    （`print("banana")` 等恢复可比）。
  - 写进 guide 的判据：**同一个机制上第二次出现同类缺陷时就停止修补**，改去证明「这个机制
    在这个约束下能不能做对」。证明方法是把约束写成链条 —— ① 这个机制必须豁免哪一类输入；
    ② 判断是否豁免的唯一可用信息是什么（只列宿主语言真的提供的原语）；③ 那个信息是否
    经过了被测差异。③ 为是则机制不可能对称，到此为止。**并写上「每次修完都自认想清楚了」
    这个感觉本身就是信号** —— 它说明在验证规则，而不是在验证机制的存在前提。
- **教训 2**（差分 harness 判据的端到端对称性：判据读的值、查表用的键、**以及它自身的执行
  成本**都必须两侧相同）：**直接升进 [[prove-the-path-under-test]] guide 正文，不再留作
  「下一实例再说」**，但放在新教训 1 之后 —— 顺序上先判断机制该不该留，再谈机制内部的
  判据对称性。理由有五条：一是同一轮内就出了**六个**同类实例（needle 用本引擎拼写 → 对称
  标记 + 不对称扫描对象 → provenance key 藏本引擎拼写 → 剥符号只剥第 1 字节 → `#t` 作预扫
  边界 → 判据自身成本不对称），另有一次同族的 pattern 假绿；二是第二到第六个实例都发生在我
  **把前面的写成教训之后**，第二次还就在我自己写的禁止性注释下面几行，第五次是我修上一条
  时引入的 —— 这证明「记一条规则」这种沉淀强度对它无效；三是它的失败模式比该 guide 现有
  条目更严重：现有条目多是「假绿」（该报没报，至少状态可疑），而单侧 skip 是**静默吞掉同一
  次运行里的真实差异**，连可疑状态都不留；四是现在有了可执行载体，可以直接把做法而不只是
  规则写进 guide；五是**第六轮把这条教训的适用范围扩了一维** —— 判据的输入不只是数据，还
  包括资源。写进 guide 的内容分三层：
  - **规则层**：把每个跳过 / 判定判据当一个表达式端到端求值，列出它读到的所有量
    （被读的字符串内容、长度、字节、**查表用的 key**、**以及遍历时猜的边界**），逐个确认
    两侧相同；只放过引擎无关的固定字面量和完全不接触被测差异的运行级标记；明确写上「决定
    是否去读某个量的开关对称，不代表判据对称」；补上第四轮的教训 ——**「归一化 key 让两侧
    落进同一桶」这类修法要检查归一化对所有位置成立，不只对最常见那一个位置成立**；再补上
    第五轮的 —— **凡是判据需要重现被测函数的遍历，它就必须猜那个函数的边界，而边界（例如
    带空洞的表的 `#t`）未必两侧一致；能改成检查被测函数的「结果」就不需要猜任何边界。**
  - **成本层（第六轮新增，必须连带写进去）**：**判据自身的执行成本也是它的输入。** 在两侧
    语言实现不同的 harness 里（一侧解释执行、一侧 C），任何随输入规模增长的检查都要多问
    一句「**另一侧付得起吗**」，这一问与「值对称吗」相互独立。三步过法：① 检查的复杂度随
    输入的哪个维度增长；② fuzz 能把那个维度推到多大；③ 开销更大的那一侧推到那么大会不会
    撞预算。本轮实证：`__sq_arg_slots` 逐字节扫 80 KiB 的 fmt（不含 NaN、不含任何转换），
    PUC 392ms 判 OK，wangshu 1m22s 后 `instruction budget exceeded`，输入连同同一次运行的
    真差异一起被丢掉。合格的写法有两条：让复杂度跟着「真正相关的东西」走（改成跟着**转换
    个数**，plain find 跳到下一个 `%`），以及**只在确实可能有东西被拦时才启动**扫描（启动
    判据每参数 O(1)、由参数类型推导所以两侧一致）；实测 38s → 264ms，与 base 216ms 同档。
  - **做法层（本轮新增，必须连带写进去）**：不要停在「要检查」——**把 gate 路径上所有
    读取点 `grep` 出来枚举成一份表，再把这份表变成一个常驻测试，并做反向验证**。本轮
    实证：`TestFuzzOracleDiff_NaNGateNoAsymmetricSkip`（现 47 种写法）断言「任何输入不得只
    在一侧 skip」，把 `#t` 预扫改回去报 `3/47 shapes decide asymmetrically`（而旧的 29 条
    一条都报不出来）；成本这一维单独立
    `TestFuzzOracleDiff_NaNGateNoCostAsymmetry`，**断言的是成本比值而不是 verdict**，每个
    形式与自己的短输入版本比以抵消共享机器负载。理由是这类错误每次换一个位置藏，靠人工
    「想清楚」拦不住，只有把枚举本身固化进 CI 才不需要等下一轮审计。**但要连带写上它的两种
    失效方式**：维度选错（29 条枚举错的维度，第四、第五轮的 blocking 照样穿过），以及断言
    选错量（成本测试第一版断 verdict，把 bug 改回去照样通过），都见教训 8。
- **教训 8**（全称断言测试的实际强度等于用例表的覆盖面 **+ 枚举测试的强度取决于枚举了
  哪个维度**）：**与教训 2 一起写进 [[prove-the-path-under-test]] 正文**，作为该清单的
  验证侧一节。两层实证都很清楚：
  - hand-picked 的一层：`TestFuzzOracleDiff_NaNGateSymmetry` 断言的正是被违反的那条
    性质，在含缺陷的代码上仍 PASS，只因为用例表里没有那个形式。
  - **枚举的一层**：`...NoAsymmetricSkip` 的 29 条里 `string.format` 的 fmt 全是裸 verb、
    表全是无空洞的，恰好是当时的缺陷能正确处理的写法，第四轮和第五轮的 blocking 都直接
    穿过。**选错维度的枚举和 hand-picked 一样弱**，条数多不等于维度覆盖全。第五轮补上
    fmt 写法 / 表写法 / 参数位置三维之后（29 → 47），同一个缺陷从「一条都报不出来」变成
    报 `3/47` —— 这个对比可以直接引用，它把「维度比条数重要」量化了。
  - **断言选量的一层（第六轮新增，必须连带写进去）**：成本这一维**根本不在原来的枚举里**，
    因为它沿的是「输入有多大」而不是「输入长什么样」，且后果不经由值、经由时钟；补它得单独
    立测试。更值得记的是第一版**断言选错了量**：只断「两侧 verdict 一致」，把 bug 改回去
    照样通过（wangshu 慢 74 倍但仍在预算内跑完）。必须断的是**成本比值**，因为那才是「输入
    再大一点就变单侧 skip」的量；verdict 只是比值越过阈值后的离散后果，用它做断言等于要求
    测试输入刚好落在阈值另一侧。改成断比值后反向验证报 645 倍 / 上限 120 倍并失败。
  - 写进去的判据：① 全称断言测试必须配反向验证；② **写枚举测试之前先问「缺陷可能沿哪几个
    维度变化」，把维度列出来再取代表值**，不要沿自己最熟的那个维度铺条数，并且要想到有些
    维度是「规模」而不是「写法」；③ **写完枚举测试之后，对每一个已知的历史缺陷各做一次
    反向验证 —— 每个都验，不是只验一种**。本轮两次栽在这上面：第四轮只验了「按值 key」，
    漏了「前缀 fmt」；第六轮只验了「verdict 是否相同」这一种断言，漏了「成本比值」。只验
    一种时通过的信息量几乎是零，它只说明测试对我刚才手里那个缺陷有效。
- **教训 11（同一处代码改到第三版还在出同类缺陷 = 抽象选错）**：**与教训 1 一起写进
  [[prove-the-path-under-test]] 正文**，紧跟在教训 1 后面 —— 教训 1 是机制层（这个机制该不该
  存在），这一条是单点代码层（这段代码该长成什么形状）。样本强度足够：吸收逻辑**六版**才
  收敛，前五版每次都「修好上一版的问题、引入一个新问题」，而收敛的那一版**更短**。写进
  guide 的判据：同一处代码第三次出现同类缺陷时，把这段代码必须满足的**全部**性质列出来
  （把前几版各自暴露的那条都算进去），再找同时满足全部性质的写法；本轮的三条约束是无重试、
  无迭代、无 offset 运算，三条一起看答案几乎唯一，单看任何一条前五版里都有某版满足它 ——
  这正是逐个打补丁会来回震荡的机制。连带写上那个信号：**如果第 N 版的改动是「加一个上限 /
  加一个特殊情况 / 换一个基准点」，它几乎肯定是第 N+1 个缺陷的来源。**
- **教训 12（「不比较那个量」往往比「把比较做对」更可行）**：**与教训 2 一起写进
  [[prove-the-path-under-test]] 正文**，作为判据对称性那一节的收尾。样本是「限制扫描规模」
  失败**五次**，五次都是比较了一个两侧不相等的长度（输出长度 / 输入长度 / 分桶长度 / 放宽
  容差的长度 / 带内容例外的输入长度），最后的解法不是第六种比较方式而是**取消比较**：把扫描
  做成不需要上限的（plain find 两侧都无 matcher 预算，需要 pattern 处只在固定长度切片上跑）。
  写进 guide 的判据三步：① 先问这个量**能不能**两侧相等（能构造出反例就别去设计容差）；
  ② 再问这个比较是**为了什么**；③ 最后问那个目的**有没有不读这个量的实现**。连带写上两条：
  本轮的「不可能相等」是**可证的** —— `#` 与 `==` 能把 `..` 强制转换带来的符号字节搬进一个
  数字，输出里连 nan 字节都不剩，任何基于内容的例外都看不见；以及反面的代价 —— 规模上限只能
  限制 harness 自己的**循环次数**，一旦让它变成「这个输入可不可比」的判据，就会像
  `__concat_scan_cap` 那一版一样把超过 4096 元素的表整类切掉。
- **教训 9**（修复的巧妙程度与引入回归的概率成正比）：本轮样本清楚（8 条修复里唯一
  引入新机制的那一条就是唯一引入回归的那一条）。仍是单轮样本，暂留观察；建议先在本仓
  沉淀成审计流程约定：修复里凡是**新增机制**的部分，在增量盲审时单独点出来让 reviewer
  重点看，并优先评估「不修、写成已知边界」这个选项。
- **教训 3**（被包装环境里的检查不能依赖被包装的原语；对称失败 = 假绿）：该家族
  第 17 个实例、第 2 个非 Go harness 载体。与教训 2 一起升进
  [[prove-the-path-under-test]]，加一节「harness 自身失效的默认表现是假绿：两侧
  共享代码出错则输出相同 → 判等」，把 #179（shell stub + rc / 反向 grep）与本轮
  （prelude 分层 + 被包装的 pattern 引擎）两个实例一起引进去。
- **教训 4**（已知差异定性的适用边界 + 可转化性检查）：跨 2 实例，建议在
  [[2026-07-24-issue173-oracle-nan-known-diff-round]] 教训 1 的三件套上补第四件
  「可转化性检查」，下次遇到定性需要收窄时并入
  [[cross-backend-semantic-fix-sweep]] 的退让侧一节。
- **教训 5**（crasher 修之前先枚举同族出口面）：与仓库既有「不做 benchmark 形状
  的窄修复」纪律一致，作为该纪律在 fuzz 处理上的实例记录，不单独升 guide。
- **教训 6**（宿主语言 hook 缺失 → 两套手法 + 明确误伤面）：本轮内样本已经走完整个
  弧线 —— `..` 与 `#` 双重不可拦 → 补一套输出端兜底 → 兜底四轮都不对称 → **兜底整套
  删除，缺口改报对称差异**。所以这条的结论要比原来更强：第二套手法不只是「覆盖面有
  上限」，而是**当它必须读取被测差异本身时，它根本不该存在**；这时候把缺口写成公开的
  已知边界 + 报对称差异，是唯一正确的做法。仍是单轮样本，暂留观察；再遇到一次「拦截
  机制缺入口」即可升成小 guide，届时把「兜底若必须读被测差异就不要兜底」和「用测试
  固化边界」一并写进去。
- **教训 10**（自己加的前置检查可能藏起既有的真差异）：本轮已有**两个实例**，可以与教训 2
  一起进 [[prove-the-path-under-test]]，写成「加检查要同时过对称性和无副作用两道」。第一个
  是第三轮的 `table.concat` 预扫抢在 `tconcat` 参数校验之前，把一条真的错误消息差异盖成
  `equal`，并且 `j=1e15` 把一个 1ms 就能报出差异的输入拖成撞预算 skip；第二个是第六轮的
  成本不对称 —— 检查本身没有语义副作用，但它**花掉的时间**把一侧推过了预算。两者的共同点
  是「检查改变了原本会发生的事」，区别是第一个改的是**错误的顺序和文本**，第二个改的是
  **资源消耗**。与教训 2 仍是不同的失效模式（那条是判据算出不同结论，这条是结论被检查自己
  改出来的），但第六轮那个实例同时属于两条 —— 说明这两条在「成本」这一维上是重叠的，写进
  guide 时要交叉引用。另外要连带写上第五轮的补记：**「把检查钳进被测函数接受的参数域」这个
  修法方向对，但钳的那个边界本身可能两侧不同；需要重现被测函数遍历时，先看能不能改成检查
  它的输出。**
- **教训 7**（自查不等于被审；修复也要过审）：本轮已有**二十六个**实例（首轮全量盲审 8 条 /
  3 blocking，第二轮增量盲审 6 条 / 1 blocking 且是修复引入的，第三轮回到全范围盲审 6 条 /
  1 blocking 且是同类错误第三次，第四轮全范围盲审 6 条 / 2 blocking 且其一是第三轮修复只修了
  一半，第五轮 4 条 / 1 blocking 又是我修上一条时引入的，第六轮 6 条 / 1 blocking 是一个全新
  维度，第 7 到第 26 轮又抓出吸收逻辑六版、长度阈值五次以及另外三个不对称维度，见第 17 节）。
  **二十六轮几乎零误报，而且 26 轮里有 13 轮抓到的缺陷是我修上一轮时引入的**，这两个数字本身
  是结论：它说明我的自查不是「差一点」，而是系统性地看不到某一类问题；也说明「审到不出问题
  为止」这个停止条件在这类改动上是必要的 —— 中间任何一次停下来都会留下已经存在的单侧 skip。
  同时它也是教训 11 / 教训 12 的来源：**当「修上一轮引入新缺陷」的比例接近一半时，问题不在
  审得够不够，在于被修的那两处代码抽象选错了。** 建议先在本仓沉淀成流程约定：
  涉及「整类不可比 / 整类跳过」的改动，提交前跑一次隔离快照盲审；**审计修完之后再跑一次
  范围收窄到修复 commit 的增量盲审**；并且**至少再回到一次全范围盲审** —— 第三轮那条
  blocking 藏在第一轮就写下的代码里，增量范围看不到它。第四轮额外证明一件事：**盲审
  连续找出同类 blocking 时，要把这个连续性本身当结论看**（教训 1），而不是把每一轮当
  一个独立的 bug 处理。跨第二个任务域后再升 guide。

## 触发场景

- **同一个机制上第二次出现同类缺陷时**（教训 1：停止修补，去证明「这个机制在这个约束下
  能不能做对」—— 列出它必须豁免的那类输入、判断是否豁免的唯一可用信息、以及那个信息是否
  经过被测差异；经过则机制不可能对称，到此为止。「每次修完都自认想清楚了」这个感觉本身
  就是信号。本轮四轮才做这一步，前三轮都在把 key 修得更聪明）；
- **同一处代码改到第三版还在出同类缺陷时**（教训 11：不要再改这一版，把这段代码必须满足的
  **全部**性质列出来 —— 包含前几版各自暴露的那条 —— 再找同时满足全部性质的写法。本轮吸收
  逻辑六版才收敛，三条约束是无重试、无迭代、无 offset 运算；单看任何一条前五版里都有某版
  满足它。信号：第 N 版的改动如果是「加一个上限 / 加一个特殊情况 / 换一个基准点」，它几乎
  肯定是第 N+1 个缺陷的来源；收敛的那一版通常反而更短）；
- **准备给一个两侧不相等的量设计容差 / 分桶 / 例外时**（教训 12：先问「能不能不读这个量」。
  容差和分桶只是把出问题的窗口挪个位置。三步 —— ① 这个量能不能两侧相等，能构造出反例就别
  设计容差；② 这个比较是为了什么；③ 那个目的有没有不读这个量的实现。本轮「限制扫描规模」
  失败五次，最终解法是取消比较、把扫描做成不需要上限的；另外规模上限只能限制 harness 自己的
  循环次数，别让它变成「这个输入可不可比」的判据）；
- **发现自己在给判据加边界猜测、加特殊情况、加归一化步骤时**（教训 1 的更早触发点：先问一次
  「这个判据的前提对不对」，别直接改算法。第五轮的正面案例 —— 预扫要复制 `tconcat` 的遍历
  就必须猜边界，而 `#t` 在带空洞的表上两侧不同；改成检查**结果**之后判据不再需要边界，方案
  反而更简单。**问前提通常不是更贵的路，是更便宜的路**）；
- **给差分 harness 加任何随输入规模增长的检查时**（教训 2 的资源维：多问一句「**另一侧付得起
  吗**」，这一问与「值对称吗」相互独立，而且它**不是一个问题而是三个** —— **时间**（一侧解释
  执行、一侧 C，逐字节扫描必然一侧先撞 step budget）、**分配量**（PUC 的 GC 在 alloc 帽内回收
  临时串，wangshu 的 arena 回收不了，103 字节的脚本就能耗尽 arena 而 PUC 706ms 跑完）、
  **matcher 步数预算**（只有 wangshu 给 pattern 重试计步，悬崖在 1 MiB 附近，而且触发时失败
  那一侧反而**更快**，所以成本比值看不见它，pcall 包住时还是假分歧）。三步过法 —— ① 检查的
  复杂度随输入的哪个维度增长；② fuzz 能把那个维度推到多大；③ 更吃紧的那一侧推到那么大会不会
  撞它自己的那道限制。修法：让复杂度跟着真正相关的东西走，只在确实可能有东西被拦时才启动
  扫描，需要 pattern 的地方只在固定长度切片上跑）；
- **写枚举测试时**（教训 8：先定维度 —— 问「缺陷可能沿哪几个维度变化」，把维度列出来再
  取代表值，别沿自己最熟的那个维度铺条数，并且要想到有些维度是「规模」而不是「写法」；
  断言也要选对量，断的必须是「再推一点就出事」的那个连续量，而不是它越过阈值后的离散后果。
  本轮 29 条枚举了 provenance 的消费时序，漏了 fmt 写法 / 表写法 / 参数位置，也完全没有
  成本这一维；成本测试第一版断 verdict，把 bug 改回去照样通过）；
- **写反向验证时**（教训 8：**对每一个已知的历史缺陷各验一次，不是只验一种**。只验一种时
  通过的信息量几乎是零 —— 它只说明测试对我刚才手里那个缺陷有效。本轮两次栽在这上面：第四轮
  只验了「按值 key」漏掉「前缀 fmt」，第六轮只验了「verdict 是否相同」漏掉「成本比值」。
  逐个验才能发现「这份测试有一整类缺陷看不见」，第五轮 `3/47` vs 旧表 `0/29` 就是这个对比）；
- **写差分 harness 的跳过判据时**（教训 2：把判据当表达式端到端求值，列出它读到的
  每一个量并逐个问「这个量在两侧一样吗」，被读的字符串内容、长度、**查表用的 key**、
  **遍历时猜的边界**、**以及判据自身的执行成本**都算；决定是否去读某个量的开关对称不代表
  判据对称；只允许引擎无关的固定字面量或完全不接触被测差异的运行级标记；记住单侧 skip 在
  「任一侧 limit 即 skip」的框架下会静默吞掉真实差异。别停在「想清楚了」——把所有读取点
  `grep` 出来枚举成测试并反向验证，本轮载体 `TestFuzzOracleDiff_NaNGateNoAsymmetricSkip`
  （69 种写法）加上按维度分立的四个：`NoCostAsymmetry`（断成本比值）、`NoAllocAsymmetry`
  （断 verdict，arena 耗尽会破坏它）、`NoMatcherBudgetAsymmetry`（断两侧一致的长度）、
  `NoLengthStraddle`（扫遍阈值窗口）；「归一化 key
  让两侧落进同一桶」这类修法要检查归一化对**所有位置**成立，不只对最常见那个位置成立；
  判据需要重现被测函数的遍历时，先看能不能改成检查它的**结果**，那样不需要猜任何边界）；
- **给差分 harness 加任何前置检查时**（教训 10：除了对称性，还要问「这个检查会不会改变
  原本会发生的事」—— 错误发生的顺序、错误的文本、执行时间三个面都过一遍；前置检查不能
  比它保护的那个函数更严格、更慢、更早报错，参数域要钳进被测实现自己已经接受的范围。
  第五轮补记：钳参数域这个方向对，但**钳的那个边界本身也可能两侧不同**，能改成检查被测函数
  的结果就不用猜边界）；
- **修审计发现的问题时，尤其是要为某个边角情况加新机制的时候**（教训 9 + 教训 2：
  新机制自带一套没被推敲过的前提，先把它单独当一次新设计审一遍，并认真评估「不修、
  写成公开的已知边界」；修复本身也要过一次范围收窄的增量盲审，教训 7）；
- **写「任何 X 都不得 Y」这类全称断言的测试时**（教训 8：断言的实际强度等于用例表的
  覆盖面，函数名里的「任何」不产生约束力；必须反向验证，故意构造一个应当触发失败的
  输入确认测试真会红）；
- 在已经被包装 / 打过桩的运行环境里写检查、断言逻辑时（教训 3：只用不会被那层改写
  的原语；差分 harness 里两侧共享代码出错的默认表现是判等，必须额外打印实际 output
  与 err 确认检查真的跑了）；
- **给某一类差异做「整类不可比」定性时**（教训 4 + 教训 7：问一句这个差异能否经由
  某操作转化成有语义的值，据此写清适用的出口范围；范围性断言必须过一次独立盲审）；
- 处理 fuzz crasher 时（教训 5：先定位机制、枚举该机制的全部出口、逐个探针，再
  决定修法粒度）；
- 设计拦截类机制时（教训 6：先确认宿主语言在所有入口都有 hook；缺口要配第二套手法
  并写明误伤面，且选「丢覆盖」而不是「判错」的一侧；连第二套也覆盖不到就写成公开的
  已知边界并用测试固化）；
- **自己验证完、准备提交前**（教训 7：自查覆盖的只是我想到的那些写法；涉及整类
  跳过 / 整类不可比就跑一次隔离快照盲审，不给它父对话和预期结论）；
- **有人提议「让 wangshu 的 NaN 文本对齐 PUC」时**（先读第 13 节：渲染层改不动，因为
  `NumberValue` 在值层就把所有 NaN 规范化成 `canonNaN`；值层也改不动，因为 x86 上每个
  产生 NaN 的浮点运算都给出与 `TagNil` 逐位相同的编码，负 quiet NaN 在这个布局里没有
  容身之处；而 PUC 本身也不是固定 `-nan`，它跟随符号位）。

## 关联

- [[2026-07-24-issue173-oracle-nan-known-diff-round]]（本轮**收窄**其「已知平台
  差异」定性的适用边界，并修其 span 机制在文本 coercion 路径上的证据缺口）
- [[2026-07-25-issue179-test-go-fuzz-retry-revive-round]]（教训 3 与其教训 1 同族：
  判据集被短路满足导致假绿；那轮载体是 shell stub，本轮是 prelude 分层）
- [[2026-07-22-oracle-format-nan-inf-round]]（PR #172 把 NaN 硬编码成 `-NAN` 的
  那一轮，本轮泄漏面的拼写来源）
- [[2026-07-23-oracle-arg-coercion-round]]（同 harness 的 coercion 分歧处理轮）
- [[2026-07-12-cgo-oracle-fuzz-round]]（cgo 内嵌 oracle + `FuzzOracleDiff` 差分
  设施的建立轮）
- [[prove-the-path-under-test]]（教训 1、教训 2、教训 3、教训 8、教训 10、**教训 11、
  教训 12** 的 promotion 目标；**教训 1 建议作为该 guide 的第一节或独立小 guide**（机制层判据
  先于机制内判据），连带写上第五轮那个正面案例（问前提比修算法更便宜）；教训 2 与教训 8 直接
  升进正文，教训 2 连带把「读取点枚举成测试 + 反向验证」的做法、以及**资源层**（判据自身消耗
  的时间 / 分配量 / matcher 步数预算都是它的输入，一侧解释执行一侧 C 的 harness 里要问
  「另一侧付得起吗」，而这是三个独立的问题）写进去，教训 8 连带把「枚举维度的选择（含「规模」
  型维度）+ 断言要选对量 + 每个已知缺陷各反向验证一次」写进去；教训 10 现有两个实例，可与
  教训 2 一起进正文，写成「加检查要同时过对称性、无副作用、成本三道」；**教训 11 紧跟教训 1**
  （单点代码层：同一处改到第三版还在出同类缺陷 = 抽象选错，先列全部约束再找同时满足的写法），
  **教训 12 收在判据对称性那一节末尾**（当一个量两侧不可能相等时，问「能不能不读它」，而不是
  设计容差或分桶））
- [[cross-backend-semantic-fix-sweep]]（教训 4 退让侧的边界补充；教训 6 的
  「读 vendored PUC 源码核实语义」纪律来源）
- issue **#184** · **#185** · issue #173 · PR #181 ·
  分支 `fix/184-185-oracle-diff-crashers`，43 个 commit（`e245c17`..`e5cb293`，26 轮盲审）·
  前 6 轮：e245c17 / 0122e51 / abdba6d / c45edca / 4d45dad / 0b6c083 / 8bf5353 /
  d0eaba1 / e293811 / d3e8d17 / 92241f4 / 4627b4d / 80025fe / 402aa4a / a7197a8 /
  2613871 / a9c79f5 / c048dc3 / d92c6b9 / c650885 / 55d880c / ab9869a ·
  第 7–26 轮的关键 commit：吸收逻辑六版 e9757c8 → d6ec704 → a041c17 → dafa90f →
  59cbad4 → 2e7dc57（后缀方向 f8af187）、长度阈值五次 898e206 → 5c9caf7 → 2c9476a →
  2e7dc57 → e5cb293、c00dcc5（concat cap 不再抬 sentinel）、426bf37（concat 判定入口不再
  依赖 tconcat 是否成功）、6574ea0 / 000738c（测试自身的判别力）·
  `internal/oracle/prelude.go`
  （现存：`preludeNaNCoercion` / `__gate1` / `__oracle_sfindraw` / `__nan_provenance`
  与 #173 的 span 通道，八处扫描点均无长度阈值、无 script 规模的循环；已删除：
  `__emit_nan_gate` 与 `__prov_key`（2613871）、`__fmt_nan_rendered`（e293811）、
  `__gate` helper（4627b4d）、`table.concat` 的表预扫（d92c6b9，改为检查结果字符串）、
  `fmt_was_nan` 分支（c650885，不可达））·
  `internal/stdlib/stringlib.go`（`stringFnFind` 的 plain 路径改用 `bytes.Index`，
  去掉每次调用复制剩余 subject；`cFormatSpecialFloat` 的 godoc 记 #173 定性的适用边界）·
  `internal/oracle/oracle_test.go`
  （`TestExec_NaNCoercionGate` / `TestExec_NaNCoercionGateKnownBoundary` /
  `TestExec_NaNGateEngineIndependentNeedle` / `TestExec_NaNSpansKnownLimit`）·
  根包 `oracle_nan_gate_test.go`，**9 个 gate 测试**
  （`TestFuzzOracleDiff_NaNGateSymmetry` / `NoFalsePositive` / `AcceptedCost` /
  `Coverage` / `NoAsymmetricSkip`（69 种写法）/ `NoCostAsymmetry`（断成本比值）/
  `NoAllocAsymmetry`（arena vs GC）/ `NoMatcherBudgetAsymmetry`（`maxMatchSteps`）/
  `NoLengthStraddle`（扫遍阈值窗口））·
  `internal/oracle/_lua515/src/lvm.c`
  `luaV_concat` 与 `OP_LEN` · `internal/value/value.go`（`NumberValue` /
  `canonNaN` / `qNanBoxBase` / `TagNil`，第 13 节 NaN-boxing 实测的对象）·
  `testdata/fuzz/FuzzOracleDiff/{7dd267cdb3e0b40a,f988b149f1af8375}`
