//go:build wangshu_trace

package crescent

const traceExec = true

// ciMirrorCheck:wangshu_trace 构建下,enterLuaFrame 压帧后回读 ci 段自检镜像
// 与 Go cis 逐字段一致(PW10 R2b-1 安全网,防打包 bug)。默认构建 false 编译消去。
const ciMirrorCheck = true
