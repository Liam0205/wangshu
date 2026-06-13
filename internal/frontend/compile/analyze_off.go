//go:build !wangshu_profile

package compile

import (
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/ast"
)

// analyzeCompilability 默认 build 下是 no-op——P1-only 部署不引入 bridge
// 包(避免增加 import 图),Proto.Compilability 留零值(等价 CompUnknown)。
func analyzeCompilability(_ *ast.FuncExpr, _ *bytecode.Proto) {}
