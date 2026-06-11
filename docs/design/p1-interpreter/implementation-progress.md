# P1 实现进度

> 状态:**值世界地基已落地**(M0-M6 共 7 个里程碑)。下文记录每个完成里程碑的产出、单测覆盖、
> lint+race 状态,与下一步衔接点。本文档随实现推进增量更新,M14 总验收通过后归档。
>
> 设计文档参考路径见 [00-overview](./00-overview.md);每个里程碑列对应文档 §。

## 已完成里程碑

| M | 内容 | 主要文件 | 单测 | 提交 |
|---|---|---|---|---|
| M0 | 工程地基:go.mod/Makefile/.githooks/ci.yml/.golangci.yml/oracle 校验脚本 | `Makefile`, `.githooks/`, `.github/workflows/ci.yml`, `scripts/check-oracle.sh`, `scripts/go-fuzz.sh` | hook 拦截手测、`make all` 通过 | `eaf944c` |
| M1 | arena 分配器:bump + grow + 双视图 backing + null GCRef 保留 + 注入点 | `internal/arena/arena.go` | 11 用例(对齐/grow/dual view/zero-len/misalign panic) | `a34c680` |
| M2 | NaN-boxed Value:8 个 tag、IsNumber/IsCollectable/Truthy、NaN 规范化、bool/lightUD/GCRef round-trip | `internal/value/value.go` | 9 用例(常量 bits / 边界 / NaN canon / 全 tag round-trip / 48-bit 截断) | `e209dba` |
| M3 | object 布局:GCHeader 位字段 + String/Table/Closure/Upvalue/Userdata/Thread 读写 helper(含 Table.gen / Upvalue.nextOpen 回填字段) | `internal/object/{header,string,table,closure,thread}.go` | 8 用例(各类型 round-trip、字数公式核对、open chain) | `0a62aab` |
| M4 | bytecode ISA:38 个 opcode + ABC/ABx/AsBx 编解码 + Proto + ICSlot + int2fb/fb2int + 02 §8 示例核对 | `internal/bytecode/{instruction,opcode,proto,floating_byte}.go` | 9 用例(全字段边界编解码 / 格式路由 / fb 编码对照 / 02 §8 序列) | `e8f24a0` |
| M5 | mark-sweep GC:STW + 双白翻转 + 显式 gray stack + R1..R9 根 + shadow stack 二元形态 + JSHash + intern 弱可达索引 + finalize/weak stub | `internal/gc/{collector,mark,sweep,shadow,intern}.go` | 9 用例(intern 命中/rehash / sweep 回收不可达 / shadow stack 保护 / 高频 GC 压力 64 串) | `336e98e` |
| M6 | lexer:21 关键字 + 全符号 + 数字(十进制+0x)+ 短字符串(全转义)+ 长字符串/长注释(共用扫描子程序)+ 四种换行行号 | `internal/frontend/{token/token,lex/lexer}.go` | 11 用例(关键字 / 标识符 / 数字 / 短长字符串 / 注释 / 行号 / 错误 prefix / 5.2+ 排除) | `c726e33` |

## 验证状态

- 全部里程碑 `make all` 通过(`gofmt -l` 空、`golangci-lint` 0 issues、`go test -race ./...` 全绿)。
- commit-msg hook 已多次拦截不合规 message(初次写错的 `bad message format` 被拦下,见 M0 流程)。
- pre-commit hook 多次拦截未格式化文件,起效。
- `make hooks` 已安装。

## 下一步衔接(剩余 8 个里程碑)

| M | 内容 | 估算 | 备注 |
|---|---|---|---|
| M7 | parser:递归下降 + 优先级爬升 + AST 节点(04 §3-§4) | 1 周内 | LL(2) 前瞻已由 lexer pull-Next + parser ahead 缓存解决 |
| M8 | codegen:expdesc/freereg + 跳转回填 + 常量折叠 + 黄金字节码测试 | 2 周内 | 04 §10 与 02 §8 逐字节一致是核心验收 |
| M9 | 解释器最小循环:算术/循环/调用,无 GC 介入(05 §1-§4/§7/§10) | 1.5 周 | reentry 模型,Lua-call-Lua 不增 Go 栈 |
| M10 | IC + safepoint + GC 接入 | 1 周 | gen bump 失效、shadow stack 在 host fn 落地 |
| M11 | 元表/错误/协程(07/09/08) | 2-3 周 | weak table mode 接入(M5 的 stub 兑现)、协程路线 B、xpcall handler |
| M12 | stdlib(10):base→string(pattern)→table→math→io 最小集 + 三层禁用(LibsSafe/Libs/Exclude) | 3 周 | string.match pattern matcher 是大头 |
| M13 | 嵌入 API + arena ABI 字段级实现(11) | 1 周 | ColInt64 超界报错 |
| M14 | conformance + difftest harness + benchmark + CI 门禁全启用(12) | 1.5 周 | 三档 ≥2x、与官方 5.1.5 逐字节一致 |

合计与 [00-overview](./00-overview.md) §3 估算的 6.5-9.5 人月吻合,剩余 ≥6 人月工作量;不在单一会话中完成。

## 下一会话开工提示

实现者(或下一会话的 agent)进入时:

1. `make hooks && make all` 核验地基状态。
2. 读 [04-frontend-parser-codegen](./04-frontend-parser-codegen.md) §3-§4 即可开工 M7。
3. lexer 已暴露契约见 `internal/frontend/lex/lexer.go` 头部 doc(回应 04 §13 第一条缺口);parser 应按 04 §4.1 实现 `Parser{tok, ahead, hasAhead}`,用 `lx.Next()` 拉。
4. 若发现某里程碑设计文档与实现不符,优先信代码、记入 `llmdoc/memory/doc-gaps.md`,提 PR 同步设计文档(评审纪律:`type(scope):` commit-msg)。

## 相关

[00-overview](./00-overview.md) · [../engineering](../engineering.md) ·
[12-testing-difftest](./12-testing-difftest.md) ·
设计 ↔ 实现的字段回填映射:Table.gen([01](./01-value-object-model.md) §5.2 ↔ `object.TableGen/BumpGen`)、
Upvalue.nextOpen([01](./01-value-object-model.md) §5.4 ↔ `object.UpvalNextOpen`)、
Proto.LocVars([01](./01-value-object-model.md) §5.7 ↔ `bytecode.LocalVar`)、
ICSlot.tableRef([02](./02-bytecode-isa.md) §7 ↔ `bytecode.ICSlot.TableRef`)。
