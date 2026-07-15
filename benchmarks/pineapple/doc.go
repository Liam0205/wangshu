// Package pineapple_bench: a benchmark for the real-world usage form of wangshu as pineapple's
// default lua backend.
//
// Design motivation: wangshu's own baseline / realworld / embedded benchmarks are all "the
// boundary-dominated forms we imagined", which may diverge from real downstream usage. pineapple
// (https://github.com/Liam0205/pineapple) is wangshu's first production consumer; its
// transform_by_lua operator drives wangshu through pine.Engine.Execute. This package runs LuaOp
// directly using pineapple's public API + a minimal pipeline configuration, reflecting wangshu's
// performance numbers under real downstream usage.
//
// Three comparison paths (all working this round):
//   - Gopher       —— pineapple with the gopher-lua backend (-tags=lua_gopher)
//   - WangshuP1    —— pineapple with wangshu's default build (crescent interpreter, P3 dead-code)
//   - WangshuP3Auto —— pineapple with wangshu's p3 build + natural hotness promotion
//
// The fourth path WangshuP3Force (force-all promotion) is **not implemented** this round and is
// left as follow-up: pineapple's wangshu pool manages the state internally and the public API does
// not expose the handle, so the wangshu bench side cannot inject SetForceAllPromote(true). This
// needs pineapple cross-repo work to expose a backend hook.
//
// Real-promotion verification: wangshu's public surface has the State.PromotionCount() testing-only
// API (=0 before the run, >0 after, proving Proto promotion happened). But the state handle inside
// pineapple's pool is likewise not exposed, so this package's bench **cannot make a white-box
// assertion**; instead it proves the path implicitly via the "p1 vs p3 number difference"
// (prove-the-path-under-test guide): p3 clearly faster than p1 → promotion happened + the wasm gain
// outweighs the sampling overhead; p3 ≈ p1 or slower → no promotion, or the gain is insufficient.
//
// wangshu's own PromotionCount() has independent verification in promotion_count_p3_test.go at the
// root of the wangshu repo (force-all + inner function → promotes).
//
// Dependency management: pineapple is temporarily cloned to .pineapple/ (hidden by .gitignore) via
// scripts/fetch-pineapple.sh. Local developers must first run `make bench-pineapple-fetch` before
// running the bench (the top-level wangshu Makefile entry; or `make fetch` via the local Makefile
// in benchmarks/pineapple/; or call scripts/fetch-pineapple.sh directly). CI fetches it in a
// workflow step. See the README for details.
package pineapple_bench
