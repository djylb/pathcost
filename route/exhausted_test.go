package route

import (
	"slices"
	"testing"

	"github.com/djylb/pathcost"
)

// A pair quota used up makes its edge cost pathcost.Exhausted more: new
// flows take another path while one exists, and the exhausted one only
// without.
func TestExhaustedAvoided(t *testing.T) {
	tp := topo(4, Demand{Src: 1, Dst: 3})
	tp.Candidates = 1
	lt := LinkTable{}
	meas(lt, 1, 3, LinkMeas{RTTus: 5_000})
	meas(lt, 1, 2, LinkMeas{RTTus: 8_000})
	meas(lt, 2, 3, LinkMeas{RTTus: 8_000})
	first := func(tp *Topo) Path {
		t.Helper()
		pt, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
		return set(t, pt, 1, 3).Paths[0]
	}
	if p := first(tp); !slices.Equal(p.Hops, []NodeID{3}) {
		t.Fatalf("direct first: %v", p.Hops)
	}
	// the direct link's quota is used up: over 2
	tp.Exhausted = map[Edge]struct{}{MakeEdge(1, 3): {}}
	if p := first(tp); !slices.Equal(p.Hops, []NodeID{2, 3}) || p.CostUs >= pathcost.Exhausted {
		t.Fatalf("exhausted link taken: %+v", p)
	}
	// the relay's link is used up as well: the direct link again, at
	// its exhausted cost (the relay is no cheaper)
	tp.Exhausted[MakeEdge(2, 3)] = struct{}{}
	if p := first(tp); !slices.Equal(p.Hops, []NodeID{3}) || p.CostUs < pathcost.Exhausted {
		t.Fatalf("both exhausted: %+v", p)
	}
}
