package route

import "testing"

// The zero LinkMode must be LinkAuto: a pair without an override, or with a
// Cover that sets only a bias, is routed from its measurements.
func TestLinkAutoIsZeroValue(t *testing.T) {
	var zero LinkMode
	if zero != LinkAuto || (Cover{}).Mode != LinkAuto || (Cover{BiasUs: 5}).Mode != LinkAuto {
		t.Fatalf("zero LinkMode is %d, want LinkAuto (%d)", zero, LinkAuto)
	}
}

// An edge in LinkAuto mode joins routing when its measurements show it is
// usable, and drops out when they do not.
func TestLinkAutoIncludesUsableEdges(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cover map[Edge]Cover
	}{
		{"no override", nil},
		{"explicit LinkAuto", map[Edge]Cover{MakeEdge(1, 3): {Mode: LinkAuto}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecoveryFixture(t)
			for e, c := range tc.cover {
				f.topo.Cover[e] = c
			}
			// The measured direct link is usable, so it is the first candidate.
			f.run(true, []NodeID{3})
			// Once it is measured down the relay replaces it.
			f.fail(f.now+1, 1, 3)
			f.run(false, []NodeID{2, 3})
		})
	}

	t.Run("forbid differs from auto", func(t *testing.T) {
		f := newRecoveryFixture(t)
		f.topo.Cover[MakeEdge(1, 3)] = Cover{Mode: LinkForbid}
		f.run(true, []NodeID{2, 3})
	})
}
