//go:build !wangshu_profile

package compile

import (
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/ast"
)

// analyzeCompilabilityWithOuter is a no-op under the default build — a P1-only
// deployment does not pull in the bridge package (avoiding growth of the import
// graph), leaving Proto.Compilability at its zero value (equivalent to
// CompUnknown).
func analyzeCompilabilityWithOuter(_ *ast.FuncExpr, _ *bytecode.Proto, _ *funcState) {
}
