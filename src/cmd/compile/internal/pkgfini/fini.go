// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkgfini

import (
	"cmd/compile/internal/base"
	"cmd/compile/internal/deadcode"
	"cmd/compile/internal/ir"
	"cmd/compile/internal/objw"
	"cmd/compile/internal/typecheck"
	"cmd/compile/internal/types"
	"cmd/internal/obj"
)

// Task makes and returns an initialization record for the package.
// See runtime/proc.go:initTask for its layout.
// The 3 tasks for initialization are:
//   1) Initialize all of the packages the current package depends on.
//   2) Initialize all the variables that have initializers.
//   3) Run any init functions.
func Task() *ir.Name {

	var deps []*obj.LSym // initTask records for packages the current package depends on
	var fns []*obj.LSym  // functions to call for package initialization

	// Find imported packages with init tasks.
	for _, pkg := range typecheck.Target.Imports {
		n := typecheck.Resolve(ir.NewIdent(base.Pos, pkg.Lookup(".finitask")))
		if n.Op() == ir.ONONAME {
			continue
		}
		if n.Op() != ir.ONAME || n.(*ir.Name).Class != ir.PEXTERN {
			base.Fatalf("bad finitask: %v", n)
		}
		deps = append(deps, n.(*ir.Name).Linksym())
	}

	if typecheck.Target.Fini != nil {
		deadcode.Func(typecheck.Target.Fini)
		fns = append(fns, typecheck.Target.Fini.Nname.Linksym())
	}

	if len(deps) == 0 && len(fns) == 0 && types.LocalPkg.Name != "main" && types.LocalPkg.Name != "runtime" {
		return nil // nothing to initialize
	}

	// Make an .finitask structure.
	sym := typecheck.Lookup(".finitask")
	task := typecheck.NewName(sym)
	task.SetType(types.Types[types.TUINT8]) // fake type
	task.Class = ir.PEXTERN
	sym.Def = task
	lsym := task.Linksym()
	ot := 0
	ot = objw.Uintptr(lsym, ot, uint64(len(deps)))
	ot = objw.Uintptr(lsym, ot, uint64(len(fns)))
	for _, d := range deps {
		ot = objw.SymPtr(lsym, ot, d, 0)
	}
	for _, f := range fns {
		ot = objw.SymPtr(lsym, ot, f, 0)
	}
	// An initTask has pointers, but none into the Go heap.
	// It's not quite read only, the state field must be modifiable.
	objw.Global(lsym, int32(ot), obj.NOPTR)
	return task
}
