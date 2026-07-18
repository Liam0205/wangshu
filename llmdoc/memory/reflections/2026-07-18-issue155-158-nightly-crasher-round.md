---
name: 2026-07-18-issue155-158-nightly-crasher-round
description: >
  2026-07-18 nightly-fuzz crasher 巡检轮(分支 fix/155-159-nightly-crashers,
  PR #160)。两个真 bug + 三个不可复现:① issue #155 是真实 P4 miscompile——
  seg2seg dispatch 依赖 emitReturnDualSemantics 的段内 teardown,它不会像解释器
  doReturn 那样把返回值补 nil 到 caller 期望的 C-1 个,callee 混合宽度 RETURN 时
  0 值路径让 caller 读到陈旧寄存器;修法是 populateCallIC 的 NeverExits 标志再加
  seg2SegRetWidthOK 一道要求(ProtoSeg2SegRetCount 只统计 CFG 可达块内的 RETURN
  宽度)。本轮最大教训:给 bridge.NativeSegAddrer 这种靠运行时类型断言满足的接口
  加方法时,只在 amd64 实现漏了 arm64 镜像,断言在 arm64 上静默失败使 seg2seg
  整体静默失效(结果仍正确、命中数归零),本地 amd64 全绿,只有双架构 CI 抓到——
  这是 cross-backend-semantic-fix-sweep 的一个特别隐蔽实例。另一坑:luac 在每个
  显式 return 后追加的死尾 RETURN 0 1 会毒化按 RETURN 宽度做的静态分析,必须先过
  CFG 可达性。② issue #158 是真实 stdlib bug:string.format %X/%x/%u/%o 对
  >= 2^63 的值,PUC 走 x86-64 gcc 的 (unsigned long long)(double) 降级序列,Go 的
  uint64(int64(f)) 饱和为 0x8000000000000000;修法 stringlib.go 加 cUnsignedCast
  模拟 C 语义(arm64 FCVTZU 饱和行为不同属 C UB 区,difftest corners 豁免)。
  ③ #156/#157/#159 为不可复现 concat storm 家族(与 #123/#144 等同族),值得记录
  的新信号:GOMEMLIMIT=512MiB 软限制没接住 41.5M execs 处的静默死亡,下一步升级
  方向是 harness 按 seed 记 wall-clock。
metadata:
  type: reflection
  date: 2026-07-18
---

# 2026-07-18 nightly crasher 巡检轮反思(#155/#156/#157/#158/#159,PR #160)

> 范围:分支 `fix/155-159-nightly-crashers`,PR #160。两个真 bug(#155 P4
> seg2seg miscompile + #158 stdlib string.format 大整数 cast)+ 三个不可复现
> crasher(#156/#157/#159,concat storm 家族)。

## 任务

处置 2026-07-18 nightly-fuzz 巡检报出的五个 crasher:

- **#155**:P4 seg2seg dispatch 在 callee 混合宽度 RETURN 时产生错值(真 miscompile);
- **#158**:`string.format` 的 `%X/%x/%u/%o` 对 >= 2^63 的值与 PUC 分歧(真 stdlib bug);
- **#156/#157/#159**:本地重放干净的 concat storm 家族(不可复现,分诊处置)。

## 期望与实际

### #155(P4 seg2seg 混合宽度 RETURN)

- 期望:seg2seg dispatch(caller 原生段直接 `call` 进 callee 段)的返回值语义
  与解释器一致。
- 实际:seg2seg 依赖 `emitReturnDualSemantics` 的段内 teardown,它只搬
  nret = RETURN.B-1 个值到 caller 的 R(A..) 窗口,无法像解释器 `doReturn` 那样
  把不足的部分补 nil 到 caller 期望的 C-1 个。callee 含混合宽度 RETURN(裸
  `return` 0 值 + `return x,(0)` 2 值)时,0 值路径让 caller 读到陈旧寄存器
  (读到的是 callee closure 本身)。
- 修法:`populateCallIC` 的 NeverExits 标志现在还要求 `seg2SegRetWidthOK`——
  `ProtoSeg2SegRetCount` 计算「CFG 可达块内所有 RETURN 的统一宽度」并与 CALL 的
  C-1 比较,宽度不统一或与 caller 期望不符则不给 seg2seg 资格。

### #158(string.format 大整数 cast)

- 期望:`%X/%x/%u/%o` 对任意 double 与 PUC 逐字节一致。
- 实际:对 >= 2^63 的值,PUC 经 `(unsigned long long)(double)` C cast,x86-64
  gcc 把它降为「f < 2^63 → cvttsd2si;否则 (f-2^63)+2^63」;Go 的
  `uint64(int64(f))` 把一切 >= 2^63 的值饱和为 `0x8000000000000000`,两边分歧。
- 修法:`internal/stdlib/stringlib.go` 加 `cUnsignedCast` 模拟 C 语义,逐点对照
  gcc -O2 探针验证 10 个用例。注意 arm64 的 FCVTZU 对越界 double 的饱和行为与
  x86 不同(这段属于 C UB 区,PUC 自己在不同架构上就不一致),这类值不进
  difftest corners,注释里写明豁免理由。

### #156/#157/#159(不可复现 concat storm 家族)

与 #123/#144/#145/#150/#151/#152 同族:worker 静默死亡、落盘 input 本地重放
干净(1.2-2.5 秒)。按 [[unreproducible-crasher-triage]] 处置,语料不入
`testdata/fuzz/`。值得记录的新信号:这几轮 run 已经带上 PR #154 的
`GOMEMLIMIT=512MiB`,worker 仍在 41.5M execs 处静默死亡——软限制没接住,说明
诊断硬化还差一层;下一步升级方向是在 harness 里按 seed 记 wall-clock,把
「哪个 seed 之后进程消失」变成可归因信息。

## 踩坑与教训

### 教训 1(本轮最大教训):运行时断言接口加方法,双架构镜像必须同批补齐——编译器不会帮你查

给 `bridge.NativeSegAddrer` 接口加 `NativeSeg2SegRetCount` 方法时,只在 amd64
的 `translator_native.go` 实现,漏掉 arm64 镜像(`translator_native_arm64.go`)。
该接口靠运行时类型断言 `code.(bridge.NativeSegAddrer)` 满足——缺一个方法就让
断言在 arm64 上**静默**失败,`NativeCalleeSegAddr` / `CalleeNeverExitsSegment` /
`CalleeSeg2SegRetCount` 三处共用该断言的调用点全部 ok==false,arm64 上 seg2seg
整体静默失效(结果仍正确,只是命中数归零)。本地 amd64 全绿,直到 CI 的
ubuntu-24.04-arm p4 leg 上 5 个 seg2seg 测试报 hits=0 才暴露。修法是 arm64 补齐
逐字一致的镜像方法(commit 5f76472)。

**Why**:显式实现的接口(struct 声明 implements)缺方法是编译错;靠运行时类型
断言满足的接口缺方法只是「断言返回 false」,是合法程序行为,编译器与 lint 都
不报。而 nativeCode 这类按 build tag 分文件的类型在 amd64/arm64 各有一份
struct,给断言接口加方法时心理边界很容易停在「当前在改的那份」。这正是
[[cross-backend-semantic-fix-sweep]] 描述的「修复心理边界停在当前后端」模式的
一个特别隐蔽的实例——隐蔽在于:漏掉的那一侧不产生错值,只产生「优化静默关闭」,
byte-equal 测试全绿,只有带命中数断言的白盒测试(prove-the-path 家族)在双架构
CI 上能抓到。

**How to apply**:给任何「靠运行时类型断言满足」的接口加方法时,同一 commit 内
grep 所有实现该接口的类型——对 P4 就是按 build tag 分文件的 amd64/arm64 两份
nativeCode struct;补齐后确认双架构 CI(尤其带白盒命中数断言的测试)全绿再算
完成。双架构 CI 是这类缺口的唯一防线,本地单架构全绿不构成任何证据。

### 教训 2:luac 的死尾 RETURN 是按 RETURN 宽度做静态分析的系统性陷阱

第一版 `ProtoSeg2SegRetCount` 遍历完整 code 数组,被 luac 在每个显式 return 后
追加的死尾 `RETURN 0 1` 毒化——所有函数看起来都是混合宽度,新加的 gate 全拒,
seg2seg 完全失效(`seg2seg_deopt_redo_test` 的 SegToSegHitCount 停摆抓到)。
改为只遍历 `buildCFG` + `reachableBlocks` 的可达块后恢复正常。

**How to apply**:任何按 RETURN(或其它终结指令)宽度/形状做的 proto 级静态
分析,必须先过 CFG 可达性过滤;luac 生成的字节码里死代码是常态(每个显式
return 之后必有一条死尾 RETURN 0 1),直接扫 code 数组的分析对它结构性误判。
这次是白盒命中数测试立刻抓住的——再次确认 prove-the-path 类断言对「优化静默
失效」是不可替代的防线(与教训 1 同一防线两次立功)。

### 教训 3:C cast 的 UB 区语义按「PUC 在参考平台上的实际行为」实现,并显式豁免架构分歧

#158 的根子是 C 的 `(unsigned long long)(double)` 对 >= 2^63 的值属于实现定义/
UB 区:x86-64 gcc 有一套降级序列,arm64 FCVTZU 是另一套饱和行为,PUC 自己跨
架构都不一致。这延续 [[cross-backend-semantic-fix-sweep]]「PUC 语义由 C 实现
定义」一节的判据,并加一个细化:当 C 侧行为本身按架构分岔时,wangshu 以参考
平台(x86-64 gcc)的行为为准手写模拟(`cUnsignedCast`),同时把分岔区的值从
difftest corners 里豁免并在注释写明理由——不要试图在一份 Go 代码里同时逐字节
等于两个互不一致的 C 行为。验证手法也值得复用:直接写 gcc -O2 探针程序拿
真值,逐点对照 10 个用例,而不是凭规范推。

### 教训 4:GOMEMLIMIT 软限制接不住的静默死亡,下一层硬化是按 seed 记 wall-clock

#156/#157/#159 的 run 已带 PR #154 的 `GOMEMLIMIT=512MiB`,worker 仍在 41.5M
execs 处无声消失——说明这族死亡未必是 Go 堆压力(软限制只影响 GC 节奏,接不住
RSS 之外的资源尽头或外部 kill)。诊断硬化按层推进:arena 帽(PR #131)→
GOMEMLIMIT(PR #154)→ 下一层是 harness 按 seed 记 wall-clock,把「进程在哪个
seed 之后消失」变成 artifact 里可读的归因线索。每一层没接住都是新信息,不是
浪费——但也说明该族问题仍未闭环,巡检时要继续观察。

## 流程

- 巡检纪律(先 CI 绿 → review 意见清完 → llmdoc 更新进同一 PR → 再合入)执行
  正常;push 后 hook 自报机制工作正常,无需手动轮询。

## Promotion 候选

- **教训 1**(运行时断言接口的双架构完整性):建议并入
  [[cross-backend-semantic-fix-sweep]] 作为一个新的具体实例——它扩展了该 guide
  的实例谱:此前四个实例都是「语义修复漏站点 → 产生错值」,本例是「接口扩面漏
  镜像 → 优化静默关闭、结果仍正确」,失败形式更隐蔽(只有白盒命中数 + 双架构
  CI 能抓),且给出可操作判据「断言接口加方法时 grep 全部按 build tag 分文件的
  实现 struct」。由 recorder 决定是否写入及落点。
- **教训 2**(luac 死尾 RETURN 毒化静态分析):首次样本暂留 memory。若后续再有
  按字节码形状做 proto 级静态分析被死代码毒化的实例,可考虑升
  [[design-claims-vs-codebase-physics]] 或独立条目。
- **教训 3**(C UB 区按参考平台实现 + 显式豁免架构分岔):属
  [[cross-backend-semantic-fix-sweep]]「PUC 语义由 C 实现定义」节的细化,首次
  样本暂留;该节下次修订时可作补充。
- **教训 4**(诊断硬化按层推进):属 [[unreproducible-crasher-triage]] 的处置
  演进记录,暂留 memory 作该族问题的最新状态数据点。

## 触发场景

- 给「靠运行时类型断言满足」的接口加方法时(教训 1:grep 全部按 build tag 分
  文件的实现 struct,双架构 CI 全绿才算完成);
- 写任何按 RETURN / 终结指令宽度做的 proto 级静态分析时(教训 2:先过
  buildCFG + reachableBlocks 可达性,luac 死尾 RETURN 是常态);
- 与 PUC 的分歧落在 C 实现定义/UB 区且 C 侧跨架构不一致时(教训 3:按参考平台
  行为手写模拟 + difftest corners 显式豁免 + gcc 探针逐点验证);
- nightly concat storm 家族再报不可复现 crasher 时(教训 4:检查当前诊断硬化
  层级,GOMEMLIMIT 已证接不住,下一层是 harness 按 seed 记 wall-clock)。

## 关联

[[cross-backend-semantic-fix-sweep]](教训 1 的落点候选 + 教训 3 承「PUC 语义
由 C 实现定义」节)· [[prove-the-path-under-test]](白盒命中数断言两次立功:
SegToSegHitCount 停摆抓死尾毒化 + arm64 hits=0 抓断言静默失败)·
[[unreproducible-crasher-triage]](#156/#157/#159 分诊依据 + 教训 4 硬化层级)·
[[2026-07-13-nightly-concat-oom-and-format-hash-round]](concat storm 家族前序 +
stringFnFormat 手写 C 语义路线的延续)· issue #155 · issue #156 · issue #157 ·
issue #158 · issue #159 · PR #160 · PR #154(GOMEMLIMIT 硬化前序)· commit
5f76472(arm64 镜像补齐)

## 附记(合入前第三轮修复:Go 侧越界转换同样按架构分岔)

llmdoc 进 PR 后,oracle-smoke 的 arm64 leg 又抓到一例:
`print(string.format("%X", 0%0))`(NaN)。根因是教训 3 的第一版
cUnsignedCast 自身还不够——`uint64(int64(f))` 这类混合表达式对越界输入
走 Go 自己的架构相关转换(amd64 CVTTSD2SI indefinite,arm64 FCVTZS 饱
和),产生了与两个 PUC 官方构建都不同的第三种行为。修复
(commit 0d31290):把 NaN、<= -2^63、>= 2^64、±inf 每个 UB 角落都写成
显式分支,让 wangshu 在所有架构上钉住 x86-64 gcc 参考行为;同时在
oracle prelude 加守卫,把无符号 verb 的 UB 范围参数改道 LimitSentinel
对称跳过(arm64 PUC oracle 在 UB 区有权与钉住的 x86 语义不一致)。

教训 3 因此升级一档:模拟 C 实现定义行为时,**Go 这一侧的越界
float→int 转换与 C 一样按架构分岔**——`uint64(f)`、`int64(f)` 对越界
输入在 amd64/arm64 上结果不同。凡是「按参考平台钉行为」的模拟函数,
输入域里每个越界角落都必须有显式分支,不能让任何一个角落漏进 Go 的
原生转换;验证也必须双架构跑(本例 amd64 全绿,只有 arm64 oracle
smoke 抓得到)。
