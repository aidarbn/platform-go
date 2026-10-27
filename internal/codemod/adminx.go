package codemod

import (
	"go/ast"
	"go/token"
	"slices"
)

const adminxPkg = Module + "/kit/adminx"

// adminxRemoved are the names v0.6.0 removed from kit/adminx together with one time
// codes, and what to do instead.
var adminxRemoved = map[string]string{
	"ErrCodeRequired": "one time codes are gone in v0.6.0: drop the handling of adminx.ErrCodeRequired",
	"ErrCodeReused":   "one time codes are gone in v0.6.0: drop the handling of adminx.ErrCodeReused",
	"ErrInvalidCode":  "one time codes are gone in v0.6.0: drop the handling of adminx.ErrInvalidCode",
	"NewTOTPSecret":   "one time codes are gone in v0.6.0: remove the call of adminx.NewTOTPSecret",
	"TOTPCode":        "one time codes are gone in v0.6.0: remove the call of adminx.TOTPCode",
	"TOTPURI":         "one time codes are gone in v0.6.0: remove the call of adminx.TOTPURI",
	"VerifyTOTP":      "one time codes are gone in v0.6.0: remove the call of adminx.VerifyTOTP",
}

// adminxUserFields are the fields v0.6.0 removed from adminx.User.
var adminxUserFields = []string{"TOTPSecret", "TOTPCounter", "TwoFactor"}

// AdminxNoTOTP follows v0.6.0, where the admin panel signs in with a password only:
// CreateUser loses the twoFactor argument and the secret result, Login loses the code
// argument. References to the removed TOTP names and fields go to a person.
func AdminxNoTOTP() Codemod {
	covers := []string{"kit/adminx.(*Auth).CreateUser", "kit/adminx.(*Auth).Login"}
	for name := range adminxRemoved {
		covers = append(covers, "kit/adminx."+name)
	}
	for _, field := range adminxUserFields {
		covers = append(covers, "kit/adminx.User."+field)
	}
	slices.Sort(covers)
	return Codemod{
		Version:     "v0.6.0",
		Name:        "adminx.no-totp",
		Description: "kit/adminx signs in with a password only: CreateUser and Login lose the one time code",
		Covers:      covers,
		Fix:         fixAdminx,
		Check:       checkAdminx,
	}
}

func fixAdminx(f *File) {
	if !f.PackageImports(adminxPkg) {
		return
	}
	// Assignments of CreateUser first: the call inside is rewritten with them.
	done := map[*ast.CallExpr]bool{}
	ast.Inspect(f.AST, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if len(n.Lhs) == 3 && len(n.Rhs) == 1 {
				if call, ok := methodCall(f, n.Rhs[0], "CreateUser", 5); ok {
					fixCreateUserAssign(f, n, call)
					done[call] = true
				}
			}
		case *ast.ValueSpec:
			if len(n.Names) == 3 && len(n.Values) == 1 {
				if call, ok := methodCall(f, n.Values[0], "CreateUser", 5); ok {
					fixCreateUserAssign(f, &ast.AssignStmt{Lhs: []ast.Expr{n.Names[0], n.Names[1], n.Names[2]}, Tok: token.ASSIGN}, call)
					done[call] = true
				}
			}
		}
		return true
	})
	ast.Inspect(f.AST, func(n ast.Node) bool {
		if call, ok := methodCall(f, asExpr(n), "CreateUser", 5); ok && !done[call] {
			dropArg(f, call, 4)
			f.Report(call, "CreateUser returns two results since v0.6.0, the user and the error: the secret of one time codes is gone; fix the use of the results by hand")
			reportTwoFactor(f, call.Args[4])
		}
		if call, ok := methodCall(f, asExpr(n), "Login", 6); ok {
			dropArg(f, call, 3)
			if lit, ok := call.Args[3].(*ast.BasicLit); !ok || (lit.Value != `""` && lit.Value != "``") {
				f.Report(call.Args[3], "Login takes no one time code since v0.6.0: the argument "+f.Source(call.Args[3])+" is dropped, remove the code from the sign in form")
			}
		}
		if sel, ok := n.(*ast.SelectorExpr); ok && slices.Contains(adminxUserFields, sel.Sel.Name) {
			if _, pkg := f.PackageRef(sel, adminxPkg); !pkg {
				f.Report(sel, "adminx.User has no "+sel.Sel.Name+" since v0.6.0, when it is that field: one time codes are gone, remove the use")
			}
		}
		return true
	})
}

func fixCreateUserAssign(f *File, assign *ast.AssignStmt, call *ast.CallExpr) {
	lhs := assign.Lhs
	secret := lhs[1]
	f.ReplaceRange(lhs[0].End(), secret.End(), "")
	if id, ok := secret.(*ast.Ident); ok && id.Name != "_" {
		f.Report(secret, "CreateUser returns no secret since v0.6.0: "+id.Name+" is gone, remove its uses")
	}
	// u, _, _ := becomes _, _ =, and u, s, err := with u and err declared before becomes
	// u, err =: there is nothing new to declare.
	if assign.Tok == token.DEFINE && !declares(assign, lhs[0]) && !declares(assign, lhs[2]) {
		f.ReplaceRange(assign.TokPos, assign.TokPos+2, "=")
	}
	dropArg(f, call, 4)
	reportTwoFactor(f, call.Args[4])
}

// declares reports whether a := declares the name rather than assigning to a variable
// declared before it in the same scope.
func declares(assign *ast.AssignStmt, e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	if !ok || id.Name == "_" {
		return false
	}
	return id.Obj == nil || id.Obj.Decl == assign
}

func reportTwoFactor(f *File, arg ast.Expr) {
	if id, ok := arg.(*ast.Ident); ok && id.Name == "false" {
		return
	}
	f.Report(arg, "CreateUser takes no twoFactor since v0.6.0: the argument "+f.Source(arg)+" is dropped, the user signs in with a password only")
}

// dropArg removes the argument at i together with the comma before it.
func dropArg(f *File, call *ast.CallExpr, i int) {
	f.ReplaceRange(call.Args[i-1].End(), call.Args[i].End(), "")
}

// methodCall matches x.name(...) with n arguments and no spread, where x is not another
// imported package: a function of a package is not a method of adminx.Auth. Without type
// checking that is as far as it goes; the rewritten files are listed by plan and apply,
// and the build after the upgrade catches a call of a project type of the same shape.
func methodCall(f *File, e ast.Expr, name string, n int) (*ast.CallExpr, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != n || call.Ellipsis.IsValid() {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return nil, false
	}
	if id, ok := sel.X.(*ast.Ident); ok && id.Obj == nil && f.importedAs(id.Name) {
		return nil, false
	}
	return call, true
}

func isBlank(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "_"
}

// checkAdminx finds what cannot compile with v0.6.0 for sure: removed package level
// names and removed fields in adminx.User literals.
func checkAdminx(f *File) {
	ast.Inspect(f.AST, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			if sel, ok := f.PackageRef(n, adminxPkg); ok {
				if msg, removed := adminxRemoved[sel.Sel.Name]; removed {
					f.Report(sel, msg)
				}
			}
		case *ast.CompositeLit:
			if sel, ok := f.PackageRef(n.Type, adminxPkg); !ok || sel.Sel.Name != "User" {
				return true
			}
			for _, elt := range n.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok && slices.Contains(adminxUserFields, key.Name) {
					f.Report(kv, "adminx.User has no "+key.Name+" since v0.6.0: one time codes are gone, remove the field")
				}
			}
		}
		return true
	})
}
