package pathcost

import (
	"go/build"
	"strings"
	"testing"
)

// TestStdlibOnly: the packages import the standard library and their own
// module only.
func TestStdlibOnly(t *testing.T) {
	for _, dir := range []string{".", "route", "probe"} {
		p, err := build.ImportDir(dir, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range append(append(p.Imports, p.TestImports...), p.XTestImports...) {
			if strings.Contains(strings.SplitN(imp, "/", 2)[0], ".") && !strings.HasPrefix(imp, "github.com/djylb/pathcost") {
				t.Errorf("%s imports %s", dir, imp)
			}
		}
	}
}
