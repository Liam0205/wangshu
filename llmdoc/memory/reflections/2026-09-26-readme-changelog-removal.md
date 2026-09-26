---
name: 2026-09-26-readme-changelog-removal
description: >
  README「语言支持」后面积了 14 段「YYYY-MM-DD 变动」（2026-07-28 到 09-03 的 13 段，加一段不带日期的
  nightly 超时说明），是每轮修复顺手追加的过程记录。分支 `docs/readme-drop-changelog`（PR #269）删掉了它们。
  删之前把每段拆成事实逐条核对，全部在设计文档、各阶段 `implementation-progress.md` 或 llmdoc 里已有记录；
  其中至少三处 README 说法已被后来的更正推翻，只是 README 这份副本没人改。核对还顺带找出几处别的过时记录并修掉：
  #262 之前的行号残留清单、nightly workflow 注释里的旧预算数字、`engineering.md` 的 job 超时说法与去重键。
  教训：**README 只写使用者要知道的现状，行为变了就原地改现状描述；每轮的过程记录写进 implementation-progress 和反思。**
metadata:
  type: reflection
  date: 2026-09-26
---

# 删掉 README 里的变更日志（2026-09-26，PR #269）

## 任务

用户问 README 里从「2026-07-28 变动」开始的一大串是什么、有没有必要写在 README 里。结论是没有必要：这些是
每轮修 nightly crasher 或调 CI 时追加的过程记录（根因、判据、harness 细节、超时预算），读者是维护者，不是使用者。
用户同意删除，前提是只在 README 里出现的事实先挪走。

## 做法与结果

- 14 段各自拆成可核对的事实，分给五个只读子代理，对每条在 `docs/design/`、`docs/embedding-tiers.md`、`llmdoc/`
  和 README 其他部分找记录，判为 COVERED / COVERED-DIFFERENT / MISSING。
- 结果：没有需要挪的使用者可感知事实。唯一一条 MISSING 是琐碎的过程细节，只在 commit `e3d8dad` 里。
- COVERED-DIFFERENT 的几条都是 README 旧、别处新：`--retry-all-errors` 被写成 curl 重试超时的前提（其实单靠
  `--retry` 就重试超时）、infra issue 按 `run_id` 去重（现在按日期）、nightly 超时那段的大部分数字
  （native 上限、`gofuzztime`、链长）已经改过。
- 顺带修掉的过时记录：p1 `04-frontend-parser-codegen.md` §5.2.2 与 `implementation-progress.md` 里 #262 之前的
  行号残留清单（三项里两项其实会 raise，#262 已全部对齐）；`nightly-diff-fuzz.yml` 里按 2026-08-29 之前共用 35m、
  上限 195/280/300 写的注释；`engineering.md` 的「每个 job 都有 job 级超时」（`uses:` job 设不了）、去重键正文与
  小标题矛盾、已补上的缺口仍写成缺口；p1 进度表与 `llmdoc/index.md` 的同类旧说法。

## 根因

没有任何规则要求往 README 追加「变动」段落。从 `git log -S` 看，第一段来自 2026-07-28 的 `e3d8dad`，之后每轮
照着前一轮的写法接着加。同一件事写了两份，后来的更正只改了设计文档或反思那一份，README 那份没有任何检查会提醒。

## 教训（可复用判据）

1. **README 只写现状，不写历史。** 行为变了，原地改描述现状的句子或表格行；过程记录进 implementation-progress、
   设计文档对应节和反思。已提升为 [[where-to-record-changes]]。
2. **一份内容多处复述，更正只会落在其中一处。** 写之前先问：这件事已经在哪里写全了？已经写全就放链接。
3. **清理重复内容时逐条核对，而不是按「应该都记过」直接删。** 核对本身会找出别处的过时记录，这次找到的比删掉的
   README 旧说法还多。

## Promotion 决策

- 新建 [[where-to-record-changes]]（guides），在 `llmdoc/index.md` guides 区登记。
- 不进 must：这是文档归属约定，不是设计前提。

## 后续

- `gofuzztime` 输入的默认值 35m 只在手动触发时生效，而 p3/p4 在 35m 下过不了 budget 预检。已在 workflow 注释里写明，
  是否改默认值等用户决定。
