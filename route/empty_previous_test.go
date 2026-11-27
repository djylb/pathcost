package route

import (
	"slices"
	"testing"
)

func TestComputeWithEmptyPreviousPaths(t *testing.T) {
	d := Demand{Src: 1, Dst: 2}
	links := LinkTable{}
	up(links, 1, 2, 1000)
	prev := &PathTable{BySrc: map[NodeID][]PathSet{1: {{Dst: 2}}}}

	got, _ := Compute(Inputs{Topo: topo(2, d), Links: links, Now: now}, prev, true)
	ps, ok := got.SetOf(d)
	if !ok || len(ps.Paths) != 1 || !slices.Equal(ps.Paths[0].Hops, []NodeID{2}) {
		t.Fatalf("paths %v, want [[2]]", hops(ps))
	}
}
