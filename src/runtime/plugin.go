// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package runtime

import "unsafe"

//go:linkname plugin_lastmoduleinit plugin.lastmoduleinit
func plugin_lastmoduleinit() (path string, syms map[string]any, errstr string) {
	var md *moduledata
	for pmd := firstmoduledata.next; pmd != nil; pmd = pmd.next {
		if pmd.bad {
			md = nil // we only want the last module
			continue
		}
		md = pmd
	}
	if md == nil {
		throw("runtime: no plugin module data")
	}
	if md.pluginpath == "" {
		throw("runtime: plugin has empty pluginpath")
	}
	if md.typemap != nil {
		return "", nil, "plugin already loaded"
	}

	for _, pmd := range activeModules() {
		//if pmd.pluginpath == md.pluginpath {
		//	md.bad = true
		//	return "", nil, "plugin already loaded"
		//}

		if inRange(pmd.text, pmd.etext, md.text, md.etext) ||
			inRange(pmd.bss, pmd.ebss, md.bss, md.ebss) ||
			inRange(pmd.data, pmd.edata, md.data, md.edata) ||
			inRange(pmd.types, pmd.etypes, md.types, md.etypes) {
			println("plugin: new module data overlaps with previous moduledata")
			println("\tpmd.text-etext=", hex(pmd.text), "-", hex(pmd.etext))
			println("\tpmd.bss-ebss=", hex(pmd.bss), "-", hex(pmd.ebss))
			println("\tpmd.data-edata=", hex(pmd.data), "-", hex(pmd.edata))
			println("\tpmd.types-etypes=", hex(pmd.types), "-", hex(pmd.etypes))
			println("\tmd.text-etext=", hex(md.text), "-", hex(md.etext))
			println("\tmd.bss-ebss=", hex(md.bss), "-", hex(md.ebss))
			println("\tmd.data-edata=", hex(md.data), "-", hex(md.edata))
			println("\tmd.types-etypes=", hex(md.types), "-", hex(md.etypes))
			throw("plugin: new module data overlaps with previous moduledata")
		}
	}
	for _, pkghash := range md.pkghashes {
		if pkghash.linktimehash != *pkghash.runtimehash && pkghash.modulename != "main" {
			md.bad = true
			return "", nil, "plugin was built with a different version of package " + pkghash.modulename
		}
	}

	// Initialize the freshly loaded module.
	modulesinit()
	typelinksinit()

	pluginftabverify(md)
	moduledataverify1(md)

	lock(&itabLock)
	for _, i := range md.itablinks {
		itabAdd(i)
	}
	unlock(&itabLock)

	// Build a map of symbol names to symbols. Here in the runtime
	// we fill out the first word of the interface, the type. We
	// pass these zero value interfaces to the plugin package,
	// where the symbol value is filled in (usually via cgo).
	//
	// Because functions are handled specially in the plugin package,
	// function symbol names are prefixed here with '.' to avoid
	// a dependency on the reflect package.
	syms = make(map[string]any, len(md.ptab))
	for _, ptab := range md.ptab {
		symName, t := ptab.name, ptab.typ
		var val any
		valp := (*[2]unsafe.Pointer)(unsafe.Pointer(&val))
		(*valp)[0] = unsafe.Pointer(t)

		name := symName.name()
		if t.kind&kindMask == kindFunc {
			name = "." + name
		}
		syms[name] = val
	}
	return md.pluginpath, syms, ""
}

func pluginftabverify(md *moduledata) {
	badtable := false
	for i := 0; i < len(md.ftab); i++ {
		entry := md.textAddr(md.ftab[i].entryoff)
		if md.minpc <= entry && entry <= md.maxpc {
			continue
		}

		f := funcInfo{(*_func)(unsafe.Pointer(&md.pclntable[md.ftab[i].funcoff])), md}
		name := funcname(f)

		// A common bug is f.entry has a relocation to a duplicate
		// function symbol, meaning if we search for its PC we get
		// a valid entry with a name that is useful for debugging.
		name2 := "none"
		entry2 := uintptr(0)
		f2 := findfunc(entry)
		if f2.valid() {
			name2 = funcname(f2)
			entry2 = f2.entry()
		}
		badtable = true
		println("ftab entry", hex(entry), "/", hex(entry2), ": ",
			name, "/", name2, "outside pc range:[", hex(md.minpc), ",", hex(md.maxpc), "], modulename=", md.modulename, ", pluginpath=", md.pluginpath)
	}
	if badtable {
		throw("runtime: plugin has bad symbol table")
	}
}

func RemoveLastModuleitabs() bool {
	var module *moduledata = nil
	for pmd := &firstmoduledata; pmd != nil; pmd = pmd.next {
		if pmd.bad {
			continue
		}
		module = pmd
	}
	lock(&itabLock)
	defer unlock(&itabLock)
	const PtrSize = 4 << (^uintptr(0) >> 63)
	for i := uintptr(0); i < itabTable.size; i++ {
		p := (**itab)(add(unsafe.Pointer(&itabTable.entries), i*PtrSize))
		m := (*itab)(*(*unsafe.Pointer)(unsafe.Pointer(p)))
		if m != nil {
			uintptrm := uintptr(unsafe.Pointer(m))
			inter := uintptr(unsafe.Pointer(m.inter))
			_type := uintptr(unsafe.Pointer(m._type))
			if (inter >= module.types && inter <= module.etypes) || (_type >= module.types && _type <= module.etypes) ||
				(uintptrm >= module.types && uintptrm <= module.etypes) {
				atomicstorep(unsafe.Pointer(p), unsafe.Pointer(nil))
				itabTable.count = itabTable.count - 1
			}
		}
	}
	return true
}

func RemoveLastModule() {
	var pre *moduledata
	for pmd := &firstmoduledata; pmd.next != nil; pmd = pmd.next {
		if pmd.bad {
			continue
		}
		pre = pmd
	}
	pre.next = nil
	lastmoduledatap = pre
	modulesinit()
}

// inRange reports whether v0 or v1 are in the range [r0, r1].
func inRange(r0, r1, v0, v1 uintptr) bool {
	return (v0 >= r0 && v0 <= r1) || (v1 >= r0 && v1 <= r1)
}

// A ptabEntry is generated by the compiler for each exported function
// and global variable in the main package of a plugin. It is used to
// initialize the plugin module's symbol map.
type ptabEntry struct {
	name name
	typ  *_type
}
