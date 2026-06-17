#!/usr/bin/env bash
# fetch-pineapple.sh —— git clone(或 git fetch + reset)pineapple 到本仓
# benchmarks/pineapple/.pineapple/,跟踪 master HEAD。
#
# 设计原则:
#   - .pineapple/ 在 benchmarks/pineapple/.gitignore 里,不进 wangshu 版本控制
#   - 不 pin commit hash,clone master HEAD;开发者本地各自 fetch 最新
#   - 数字随 pineapple master 漂——这是有意的「下游真实形态」,pineapple 落地
#     新 adapter 优化时 wangshu bench 数字就会跟上,反之亦然
#   - CI 在 workflow 步骤里 fetch,本地开发者每次跑 bench 前自己 fetch 一遍
#   - idempotent:已存在 .pineapple/ 就 fetch + reset,不重复 clone
#
# 风险记录(用户已确认接受):pineapple master 可能引入 breaking change 让
# wangshu bench 编不过——届时需要本地同步看 pineapple 改了什么;wangshu
# 自身的功能测试(make all)不受影响,因为 .pineapple/ 只被 benchmarks/
# pineapple/ 这个独立子模块依赖。
set -euo pipefail

PINEAPPLE_REPO="${PINEAPPLE_REPO:-https://github.com/Liam0205/pineapple.git}"
PINEAPPLE_BRANCH="${PINEAPPLE_BRANCH:-master}"

# 脚本所在目录的上一级 = benchmarks/pineapple/,clone 落点在 .pineapple/
script_dir="$(cd "$(dirname "$0")" && pwd)"
mod_dir="$(cd "$script_dir/.." && pwd)"
target="$mod_dir/.pineapple"

if [ -d "$target/.git" ]; then
    echo "[fetch-pineapple] $target 已存在,更新 $PINEAPPLE_BRANCH..."
    git -C "$target" fetch --depth=1 origin "$PINEAPPLE_BRANCH"
    git -C "$target" reset --hard "origin/$PINEAPPLE_BRANCH"
else
    echo "[fetch-pineapple] 首次 clone $PINEAPPLE_REPO 到 $target..."
    git clone --depth=1 --branch "$PINEAPPLE_BRANCH" "$PINEAPPLE_REPO" "$target"
fi

# 报告当前锁定的 commit(方便 debug 数字漂动)
head_sha=$(git -C "$target" rev-parse HEAD)
head_msg=$(git -C "$target" log -1 --format='%s')
echo "[fetch-pineapple] HEAD = $head_sha"
echo "[fetch-pineapple] msg  = $head_msg"
