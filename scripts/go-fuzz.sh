#!/usr/bin/env bash
# go-fuzz.sh <fuzztime> [build_tags]:自动发现全部 func Fuzz* 目标,各跑一轮冒烟。
# engineering.md §1。
#
# build_tags(可选,默认空 = 默认 build):传给 go test -tags。典型用法:
#   ./scripts/go-fuzz.sh 30s                          # 默认 build(新月解释器)
#   ./scripts/go-fuzz.sh 30s "wangshu_p3 wangshu_profile"  # 凸月 build(force-all 升层后路径)
set -euo pipefail

fuzztime="${1:-30s}"
tags="${2:-}"
found=0

tags_arg=()
if [ -n "$tags" ]; then
    tags_arg=(-tags "$tags")
fi

while IFS=: read -r file line decl; do
    func=$(echo "$decl" | sed -E 's/^func (Fuzz[A-Za-z0-9_]*).*/\1/')
    pkg=$(dirname "$file")
    found=1
    echo "fuzz: $pkg :: $func ($fuzztime) tags=${tags:-default}"
    # `"${arr[@]+"${arr[@]}"}"` 是 set -u 下「空数组安全展开」惯用法——macOS
    # bash 3.2(Apple 不升级 GPLv3 包)在 set -u 下展开空数组 `${arr[@]}` 触发
    # unbound variable 致命错;`${arr[@]+...}` 仅在数组已设时展开,空时跳过。
    go test "${tags_arg[@]+"${tags_arg[@]}"}" "./$pkg" -run='^$' -fuzz="^${func}\$" -fuzztime="$fuzztime" -timeout=120s -parallel=4
    # --exclude-dir 排除:
    #   - benchmarks:独立子模块,自家 fuzz 目标已通过 benchmarks/ 单独路径覆盖
    #   - benchmarks/pineapple/.pineapple:pineapple 仓临时 clone 落点,属另一 module,
    #     go test 跑会报 "main module does not contain package"
done < <(grep -rn --include='*_test.go' \
    --exclude-dir=benchmarks \
    --exclude-dir=.pineapple \
    -E '^func Fuzz[A-Za-z0-9_]*\(f \*testing\.F\)' . 2>/dev/null || true)

if [ "$found" -eq 0 ]; then
    echo "no fuzz targets found (func Fuzz*),skip"
fi
