package pathcost

import (
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Check sources in both modules, including tests and platform-specific files.
func TestStdlibOnly(t *testing.T) {
	const module = "github.com/djylb/pathcost"
	allowed := map[string]bool{module: true, module + "/probe": true, module + "/route": true}

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly|parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, spec := range f.Imports {
			imp, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if allowed[imp] {
				continue
			}
			if !publicStdlib(imp) {
				t.Errorf("%s imports %s", path, imp)
			} else {
				allowed[imp] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func publicStdlib(imp string) bool {
	if !fs.ValidPath(imp) || strings.Contains(imp, `\`) || strings.HasPrefix(imp, "cmd/") {
		return false
	}
	ctxt := build.Default
	dir := filepath.Join(ctxt.GOROOT, "src")
	for part := range strings.SplitSeq(imp, "/") {
		if part == "internal" || part == "vendor" || part == "testdata" || strings.HasPrefix(part, ".") || strings.HasPrefix(part, "_") {
			return false
		}
		// Match the installed spelling even on a case-insensitive filesystem.
		entries, err := os.ReadDir(dir)
		if err != nil || !slices.ContainsFunc(entries, func(e fs.DirEntry) bool { return e.Name() == part && e.IsDir() }) {
			return false
		}
		dir = filepath.Join(dir, part)
	}
	pkg, err := ctxt.ImportDir(dir, 0)
	if _, excluded := err.(*build.NoGoError); excluded {
		ctxt.UseAllFiles = true // a package for another target, such as syscall/js
		pkg, err = ctxt.ImportDir(dir, 0)
	}
	return err == nil && pkg.Goroot && pkg.Name != "main" && len(pkg.GoFiles)+len(pkg.CgoFiles) > 0
}

func TestPublicStdlib(t *testing.T) {
	for _, imp := range []string{"fmt", "syscall/js"} {
		if !publicStdlib(imp) {
			t.Errorf("rejected standard library package %q", imp)
		}
	}
	for _, imp := range []string{"cmd/internal/obj", "internal/abi", "net/http/internal", "vendor/net/http", "Fmt", `net\http`, "../fmt"} {
		if publicStdlib(imp) {
			t.Errorf("accepted non-public import %q", imp)
		}
	}
}
