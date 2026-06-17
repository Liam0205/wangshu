# pineapple_bench:wangshu 作 pineapple 默认 lua backend 时的真实使用形态 benchmark

四路对照 wangshu 在 pineapple `transform_by_lua` operator 下的性能,反映**下游真实用法**而不是 wangshu 自家想象的 boundary-dominated 形态。

## 四路

- **Gopher** —— pineapple 用 gopher-lua backend(基线对照)
- **WangshuP1** —— pineapple 用 wangshu 默认 build(新月解释器,P3 dead-code)
- **WangshuP3Auto** —— pineapple 用 wangshu p3 build(`wangshu_p3 wangshu_profile`),自然热度升层

> 想要的 **WangshuP3Force**(force-all 升层)路在本轮**没法实现**——pineapple 的 wangshu pool 不暴露 state 句柄,我们够不到给 state 注入 `SetForceAllPromote(true)`。可经 pineapple cross-repo follow-up 暴露 hook,留 follow-up。

## 一次性 setup

```bash
# 从 GitHub 临时拉 pineapple master HEAD 到 .pineapple/(隐藏目录,.gitignore 排除)
./scripts/fetch-pineapple.sh
```

`.pineapple/` 在 `.gitignore` 里——不进 wangshu 版本控制。开发者各自 `fetch` 最新 master(也意味着数字随 pineapple 漂——这是有意的「下游真实形态」)。

## 跑 benchmark

```bash
# WangshuP1(默认 wangshu build,即 pineapple 默认 lua backend)
go test -bench=. -benchmem -count=3 .

# WangshuP3Auto(wangshu p3 build,自然热度升层)
go test -tags "wangshu_p3 wangshu_profile" -bench=. -benchmem -count=3 .

# Gopher(pineapple 切 gopher-lua backend)
go test -tags lua_gopher -bench=. -benchmem -count=3 .
```

或者用 `Makefile`(本目录;顶层 wangshu Makefile 也有同款入口,见末尾「与顶层 wangshu Makefile 集成」节):

```bash
make fetch          # = ./scripts/fetch-pineapple.sh
make bench          # 三 build 全跑
```

## 设计取舍

- **最简 pipeline**(1 recall_static + 1 transform_by_lua):DAG / I/O 开销几乎为 0,LuaOp 占比 ≈100%,避免 pipeline framework 稀释凸月差异。pineapple 自家 reflection 证明 calibrated_for_you 38-op 完整管线下两 backend 差异被稀释到 ±5-7% 噪声;最简 pipeline 把这个稀释拿走
- **L2 arithmetic 形状**(`function f() return item_price * 0.85 + 10.0 end`):per-item 跨界 + 极轻计算,boundary-dominated。对位 pineapple bench L2 用例
- **N=1000 items**:足够让 wangshu HotEntryThreshold 触发自然升层(若机制工作)
- **engine 复用**:每 b.N 内重用 pine.Engine,只测 Execute 这一段
- **不跑 force-all**:pineapple pool 内部管理 state,公共 API 不暴露;auto-lifting 是 wangshu 在 pineapple 下的真实形态

## 跑前自测(prove-the-path-under-test)

wangshu `v0.x.x` 加了 testing-only `State.PromotionCount()` API,见 wangshu repo 根目录 `promotion_count_p3_test.go` 验「p3 build 下能升层 + p1 build 下永远不升」。本目录 benchmark 因为 state 句柄不可达,**靠 p1 vs p3 数字差异隐式证升层**:
- p3 明显快于 p1 → 升了,wasm 内核收益压倒采样开销
- p3 ≈ p1 → 升了但收益 ≈ 采样开销,净零
- p3 慢于 p1 → 采样钩白吃开销(可能升了但 wasm 收益不够,可能根本没升)

实测此次形态下 p3 比 p1 慢 ~3-4% —— 印证 boundary-dominated 嵌入下 p3 净开销(wangshu llmdoc `must/design-premises` 前提一/前提二的负面侧实证)。

## 关于 .pineapple/ 不进版本控制的风险

接受 pineapple master 可能 breaking change 让本 bench 编不过的风险 —— 改用户已确认接受。如果撞上,要么本地 checkout pineapple 老 commit 临时跑、要么等本 bench 跟进改。**wangshu 自身的功能测试(make all)不受影响**,因为 `benchmarks/pineapple/` 是独立子模块,主流程不依赖它。

## 与顶层 wangshu Makefile 集成

顶层 wangshu Makefile 有对应转发入口,无需 `cd` 进本目录:

| 顶层 wangshu Makefile | 本目录(`benchmarks/pineapple/Makefile`) |
|---|---|
| `make bench-pineapple-fetch` | `make fetch` |
| `make bench-pineapple` | `make bench` |

两条同款行为(顶层经 `$(MAKE) -C benchmarks/pineapple ...` 转发)。
