package controller

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

// These are the only mutating endpoints used by the IPAero line executor.
// If any of them stops proving the desired state through the hot-only
// reconciler, CI must fail before an order can disconnect every customer.
func TestProvisioningEndpointsRequireHotOnlyReconciliation(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate controller source")
	}
	source := filepath.Join(filepath.Dir(file), "client.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), source, nil, 0)
	if err != nil {
		t.Fatalf("parse client controller: %v", err)
	}

	required := map[string]bool{
		"create":     false,
		"update":     false,
		"attach":     false,
		"detach":     false,
		"delete":     false,
		"bulkCreate": false,
	}
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if _, tracked := required[fn.Name.Name]; !tracked {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch calledName(call.Fun) {
			case "requireClientMutationHotApply":
				required[fn.Name.Name] = true
			case "SetToNeedRestart":
				t.Errorf(
					"%s schedules an Xray restart; provisioning must return a retryable failure instead",
					fn.Name.Name,
				)
			}
			return true
		})
	}
	for name, guarded := range required {
		if !guarded {
			t.Errorf("%s lost the provisioning restart redline", name)
		}
	}
}

func calledName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return value.Sel.Name
	default:
		return ""
	}
}
