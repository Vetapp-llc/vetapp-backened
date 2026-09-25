package handlers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// Every PetHandler method that takes a pet id from the URL must scope
// that pet to the caller's clinic before returning anything about it.
//
// This is enforced by static inspection rather than a live request
// because the failure it guards against is an omission: `History` and
// `Certificate` simply never consulted the caller's claims, so a vet at
// one clinic could read another clinic's medical records by changing
// the id — and ids are sequential, so enumerating them was trivial.
// A test that only exercises the handlers that DO check would have
// stayed green throughout.
func TestPetHandlersAreClinicScoped(t *testing.T) {
	// Methods that read or mutate a single pet identified by the path.
	mustScope := map[string]bool{
		"Get":         true,
		"Update":      true,
		"Delete":      true,
		"History":     true,
		"Certificate": true,
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "pets.go", nil, 0)
	if err != nil {
		t.Fatalf("parse pets.go: %v", err)
	}

	found := map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Body == nil {
			continue
		}
		name := fn.Name.Name
		if !mustScope[name] {
			continue
		}
		found[name] = true

		var body strings.Builder
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.BasicLit:
				body.WriteString(v.Value)
			case *ast.Ident:
				body.WriteString(v.Name)
			case *ast.SelectorExpr:
				body.WriteString(v.Sel.Name)
			}
			body.WriteString(" ")
			return true
		})
		src := body.String()

		// canAccessPet admits the pet's own clinic and clinics that have
		// treated it; Delete is admin-only instead.
		scoped := strings.Contains(src, "canAccessPet") ||
			(name == "Delete" && strings.Contains(src, "isAdmin"))

		if !scoped {
			t.Errorf("PetHandler.%s does not scope the pet to the caller's clinic — "+
				"a vet at another clinic could read this pet by id", name)
		}
	}

	for name := range mustScope {
		if !found[name] {
			t.Errorf("PetHandler.%s not found in pets.go — was it renamed? "+
				"Update this test so the scoping guarantee isn't silently dropped", name)
		}
	}
}
