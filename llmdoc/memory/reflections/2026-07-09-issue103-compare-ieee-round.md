---
name: 2026-07-09-issue103-compare-ieee-round
description: >
  issue #103 修复轮(2026-07-09,PR #104):P4 inline 比较快路径漏 IEEE 边值语义。amd64 LT/LE 的 UCOMISD +
  裸 jcc 把 unordered(NaN)解析到错误后继(四种 op/A 组合全反)——arm64 在 2026-07-03 端口时就用 FP 安全
  条件码族修过同类,amd64 从未回头对齐;两 arch 的 EQ inline 位比较漏 canonNaN(NaN==NaN 误 true)与
  ±0(-0.0==0.0 误 false)两个 IEEE 例外。核心教训:① 双后端修 bug 时「同类站点扫描」必须跨 arch 反向
  执行——arm64 修好的类别要回头查 amd64 是否同病;② canonNaN 规范化是把双刃剑:它保证了值世界位模式唯一,
  也恰恰使位相等比较对 NaN 失效;③ 一个 fuzz 失败的根因调查要顺手扫同 family 的全部 inline 站点(LT/LE
  查完顺手查 EQ,多挖出两个潜伏 bug)。
metadata:
  type: reflection
  date: 2026-07-09
---

# issue #103 修复轮反思(2026-07-09,PR #104)

> 范围:master 合入 PR #101 后的 push CI 上,`FuzzAutoPromote` seed `765ba4598e721c69` 撞出 P1/P4 tier
> divergence(P1 stack overflow / P4 正常返回)。定位 → issue #103 → 修复 amd64 LT/LE unordered +
> 两 arch EQ 的 NaN/±0 → PR #104(CI 全绿,review 一轮 APPROVE 零问题)。

## 任务

fuzz seed `local function fib(n)if n<0 then return end fib(n)end fib(0%0)`:`0%0` 产 NaN,PUC 语义
`NaN < 0` 为 false → 无限自递归 → stack overflow 是正确结果。P4 amd64 inline 把 `NaN < 0` 判成 true,
递归提前终止。

## 根因(两类,一次调查全揪出)

1. **amd64 LT/LE(`inlineNumericCompare`)**:`UCOMISD` + 单条裸 jcc。NaN(unordered)置
   ZF=CF=PF=1,四种 op/A 组合(jb/jae/jbe/ja)全部落到错误后继。修法:关系 jcc 前加 `jp`(PF=1 =
   unordered)预分支到「条件为 false」侧的后继——x86 没有单条 jcc 能同时测有序关系并把 unordered 路由到
   指定侧。
2. **两 arch EQ(`inlineRawEq` / `inlineRawEqArm64`)**:64 位裸位相等比较,IEEE 两个例外全漏:
   - **canonNaN**:值世界把所有 NaN 规范成唯一位模式(`0x7FF8...`),两个 NaN 操作数位相同 →
     `NaN == NaN` 误判 true(PUC:false);
   - **±0**:`-0.0` 与 `+0.0` 位不同 → `-0.0 == 0.0` 误判 false(PUC:true)。
   修法:位相等且值为 canonNaN → 条件 false 侧;位不等但两操作数 OR 后幅值为零(至多符号位)→ 条件
   true 侧。**成本用 K 操作数门控**:K ≠ canonNaN 钉死相等位值(跳过 NaN 检查)、K 幅值非零不可能配
   ±0(跳过零检查),`x == 1` / `x == "key"` 常见形状保持原两分支形式零开销。

## 核心教训

### 教训 1(跨 arch 反向同类扫描——arm64 修过的类别要回头查 amd64)

arm64 的 `inlineNumericCompareArm64` 在 2026-07-03 端口轮(issue #37 step 7)就明确写了「naive 条件码
unordered WRONG」,换成了 FP 安全族 MI/PL/LS/HI。**当时没有回头查 amd64 是否同病**——amd64 的裸 jcc 从
PJ10 native(2026-07-01)一直带病到今天。方向性盲区:端口轮的心态是「把 amd64 的东西搬到 arm64,顺手修
arm64 特有的问题」,但「arm64 特有」的判断可能是错的——unordered 语义在两个 ISA 上都需要显式处理,只是
错法不同。**可操作纪律:在一个后端修掉某类语义 bug 时,立即反向检查另一后端的同类站点;端口轮里"顺手修
的"每一处都要问「这真是目标 arch 特有,还是源 arch 也有但没人看」**。这与
[[2026-07-08-issue67-amd64-nodehit-crossrun-round]] 的方向相反但同构(那次是 arm64 修好的 guard 改良
没有移植回 amd64)——两个实例已构成「双后端修复不对称」模式,建议下次再遇时升 guide。(2026-07-10 更新:第 3 实例 issue #107 已触发,升为 [[cross-backend-semantic-fix-sweep]]。)

### 教训 2(canonNaN 规范化是双刃剑——不变式的受益者可能也是受害者)

canonNaN 规范化(值世界所有 NaN 唯一位模式)本是 NaN-boxing 的守护不变式:防外部负 NaN 渗入 tag 空间。
但**正是这个不变式使 EQ 的位相等比较对 NaN 失效**——若 NaN 位模式随机,两个 NaN 位相等的概率几乎为零,
bug 反而藏不住;规范化让它 100% 复现却 100% 静默(比较"成功"了,只是语义错)。审计一条不变式时,除了问
「谁依赖它成立」,还要问「**谁的正确性恰好依赖它不成立**」——位相等 ≠ 语义相等的所有站点(EQ inline、
IC key 比较、常量表去重)都值得在 canonNaN 语境下重审。常量表去重(negzero_fold 系列 conformance 用例)
早就撞过 ±0 的同款问题,是本轮 ±0 检查的先例。

### 教训 3(一个 fuzz 失败顺手扫全 family——LT/LE 查完顺手查 EQ,多挖两个)

fuzz 只撞出 LT 的 NaN 反转,但根因调查写探针时顺手把 EQ/NEQ/±0 一起扫了(9 个形状一次跑完),多挖出
EQ 的两个潜伏 bug(NaN==NaN、-0.0==0.0,两 arch 都中)。**inline 快路径的 bug 从不孤立——同一 family
(比较/算术/加载)共享同一套「绕过 host 语义」的风险面,一个站点出 IEEE 边值问题,兄弟站点大概率也有**。
成本极低(探针多写 6 个 case),收益是把三个 bug 合进一个 PR 一次修完,而不是等 fuzz 再撞两次。

## 其它

- **#92/#94 已被 PR #100 关闭**:goal 原文要求"在 PR 里关闭 #92/#94",但核实后两者已闭,本 PR 关闭的
  是新发现的 #103,PR 描述里写明了这一偏差——goal 描述过时时,以仓库实况为准并显式说明,不硬凑。
- review 一轮 APPROVE 零问题(逐点核对了 ORR 编码、RCX 寄存器纪律、jp 预分支、CBZ 复用 rel19 fixup、
  skip 条件边界)——协议镜像 + 成本门控的设计在 review 面前自解释。

## 关联

[[2026-07-09-pr101-session-side-findings]](同日前序:#102 的发现路径)· [[2026-07-08-issue67-amd64-nodehit-crossrun-round]]
(教训 1 的反向实例:arm64 guard 改良未移植回 amd64)· [[prove-the-path-under-test]](tier-divergence
fuzz harness 正是该 guide 的活体现)· issue #103 · PR #104 · `internal/gibbous/jit/peroptranslator`
(`inlineNumericCompare` / `inlineRawEq` / `inlineRawEqArm64`)· `internal/value`(canonNaN)
