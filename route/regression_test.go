package route

import (
	"maps"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/djylb/pathcost"
)

func TestCompletePathPricing(t *testing.T) {
	tp := topo(3, Demand{Src: 1, Dst: 3})
	tp.Candidates = 1
	lt := LinkTable{}
	up(lt, 1, 3, 25000)
	for _, e := range []Edge{{1, 2}, {2, 3}} {
		meas(lt, e.A, e.B, LinkMeas{RTTus: 1000, RateOutBps: 1e6})
	}
	pt, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, false)
	p := set(t, pt, 1, 3).Paths[0]
	if p.CostUs != 18384 || !slices.Equal(p.Hops, []NodeID{2, 3}) {
		t.Fatal(p)
	}
}

func TestVisitedPrefixAndInteriorRecovery(t *testing.T) {
	tp := topo(6, Demand{Src: 1, Dst: 6, Via: []NodeID{5}})
	tp.MaxHops = 5
	lt := LinkTable{}
	for _, e := range []Edge{{1, 2}, {2, 4}, {4, 5}, {2, 5}, {2, 6}} {
		up(lt, e.A, e.B, 1000)
	}
	up(lt, 1, 3, 5000)
	in := Inputs{Topo: tp, Links: lt, Now: now}
	pt, _ := Compute(in, nil, false)
	up(lt, 3, 4, 5000)
	pt, _ = Compute(in, pt, false)
	if got := set(t, pt, 1, 6, 5).Paths; len(got) != 1 || !slices.Equal(got[0].Hops, []NodeID{3, 4, 5, 2, 6}) {
		t.Fatal(got)
	}
}

func TestSharedTopologyAndDemandIdentity(t *testing.T) {
	a := Demand{Src: 1, Dst: 2}
	b := a
	b.Allow = []NodeID{}
	for _, ds := range [][]Demand{{a, b}, {b, a}} {
		tp := topo(2, ds...)
		tp.Switch = pathcost.Hysteresis{}
		before := *tp
		lt := LinkTable{}
		up(lt, 1, 2, 1000)
		if len(tp.Demand) != 2 || a.key() == b.key() {
			t.Fatal(tp.Demand)
		}
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() {
				pt, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, false)
				if _, ok := pt.SetOf(a); !ok {
					t.Error("missing unrestricted path")
				}
				if _, ok := pt.SetOf(b); ok {
					t.Error("empty allow matched")
				}
			})
		}
		wg.Wait()
		if !reflect.DeepEqual(before, *tp) {
			t.Fatal("topology changed")
		}
	}
}

func TestDirectionalUpdatesAndExpiry(t *testing.T) {
	tp := topo(2, Demand{Src: 1, Dst: 2})
	tp.ProbeIntervalMS = 1
	edge := MakeEdge(1, 2)
	in := Inputs{Topo: tp, Now: 20, Links: LinkTable{edge: {{At: 5, Up: true, RTTus: 100000}, {At: 20, Up: true, RTTus: 10000, LossPPM: 1000}}}}
	pt, _ := Compute(in, nil, false)
	last := set(t, pt, 1, 2).Paths[0]
	in.Now++
	pt, _ = Compute(in, pt, false)
	next := set(t, pt, 1, 2).Paths[0]
	if next.CostUs >= last.CostUs {
		t.Fatal("one-sided expiry missed")
	}
	s := in.Links[edge]
	s[1].NoDatagram = true
	in.Links[edge] = s
	pt, _ = Compute(in, pt, false)
	p := set(t, pt, 1, 2).Paths[0]
	if p.RtCostUs <= next.RtCostUs || p.CostUs != next.CostUs {
		t.Fatal("carrier change missed")
	}
	s[1].RateInBps = 1000
	in.Links[edge] = s
	pt, _ = Compute(in, pt, false)
	if set(t, pt, 1, 2).Paths[0].BulkCostUs <= p.BulkCostUs {
		t.Fatal("directional bandwidth change missed")
	}
}

func TestThirdPathDuringRecovery(t *testing.T) {
	tp := topo(4, Demand{Src: 1, Dst: 4})
	tp.ProbeIntervalMS = 1
	lt := LinkTable{}
	up(lt, 1, 4, 10)
	up(lt, 1, 2, 100)
	up(lt, 2, 4, 100)
	in := Inputs{Topo: tp, Links: lt, Now: now}
	pt, _ := Compute(in, nil, false)
	down(lt, 1, 4)
	pt, _ = Compute(in, pt, false)
	up(lt, 1, 3, 20)
	up(lt, 3, 4, 20)
	pt, _ = Compute(in, pt, false)
	if pt.Recheck != now+3 {
		t.Fatal(pt.Recheck)
	}
	in.Now = pt.Recheck
	pt, _ = Compute(in, pt, false)
	ps := set(t, pt, 1, 4)
	if !slices.Equal(ps.Paths[0].Hops, []NodeID{3, 4}) || ps.PreferFirst || len(pt.pending) != 1 {
		t.Fatal(ps, pt.pending)
	}
}

func TestConstrainedProbePlans(t *testing.T) {
	tp := topo(6, Demand{Src: 1, Dst: 6, Allow: []NodeID{1, 3, 6}}, Demand{Src: 1, Dst: 6, Allow: []NodeID{1, 4, 6}}, Demand{Src: 5, Dst: 6})
	tp.Nodes[5] = Node{ID: 5}
	tp.RelayProbe = 1
	tp.ProbeIntervalMS = 1
	in := Inputs{Topo: tp, Now: 1}
	pt, _ := Compute(in, nil, false)
	in.Now++
	pt, _ = Compute(in, pt, false)
	if !slices.Equal(pt.Probe[1], []NodeID{3, 4, 6}) || len(pt.Probe[5]) > 0 {
		t.Fatal(pt.Probe)
	}
}

func TestExactSegmentsAndClone(t *testing.T) {
	tp := topo(2, Demand{Src: 1, Dst: 2})
	in := Inputs{Topo: tp, Now: now, Links: LinkTable{}}
	m := LinkMeas{RTTus: ^uint32(0), QueueUs: 1000, LossPPM: 499999}
	meas(in.Links, 1, 2, m)
	pt, _ := Compute(in, nil, false)
	ps := set(t, pt, 1, 2)
	cp := ps.Clone()
	cp.Paths[0].Links[0].QueueUs = 0
	if ps.Equal(cp) || ps.Paths[0].Links[0].QueueUs != 1000 {
		t.Fatal("shared links")
	}
	cp.Paths[0].Hops[0] = 9
	if ps.Equal(cp) || ps.Paths[0].Hops[0] != 2 {
		t.Fatal("mutable snapshot")
	}
	raw, _, _ := in.Link(1, 2)
	last := pathcost.Link{RTTus: 1}
	for _, c := range []pathcost.Class{pathcost.Interactive, pathcost.Bulk, pathcost.Realtime} {
		if pathcost.Path(slices.Concat(ps.Paths[0].Links, []pathcost.Link{last}), c) != pathcost.Path([]pathcost.Link{raw, last}, c) {
			t.Fatal(c)
		}
	}
}

func TestCandidateOracle(t *testing.T) {
	// Every permutation prefix of {2,3,4} gives a simple path from 1 to 5.
	tp := topo(5, Demand{Src: 1, Dst: 5})
	for _, limit := range []int{2, 4} {
		tp.MaxHops = limit
		lt := LinkTable{}
		for a := NodeID(1); a < 5; a++ {
			for b := a + 1; b <= 5; b++ {
				meas(lt, a, b, LinkMeas{RTTus: uint32(200 * (a + b)), RateOutBps: uint64(a*b) * 100000, LossPPM: uint32(10 * a * b), NoDatagram: a%2 == 0})
			}
		}
		in := Inputs{Topo: tp, Now: now, Links: lt}
		var paths [][]NodeID
		for _, perm := range [][]NodeID{{2, 3, 4}, {2, 4, 3}, {3, 2, 4}, {3, 4, 2}, {4, 2, 3}, {4, 3, 2}} {
			for n := 0; n <= min(len(perm), tp.MaxHops-1); n++ {
				h := append(slices.Clone(perm[:n]), 5)
				if !slices.ContainsFunc(paths, func(x []NodeID) bool { return slices.Equal(x, h) }) {
					paths = append(paths, h)
				}
			}
		}
		links := func(h []NodeID) ([]pathcost.Link, []Edge) {
			var ls []pathcost.Link
			var es []Edge
			src := NodeID(1)
			for _, v := range h {
				l, _, _ := in.Link(src, v)
				ls = append(ls, l)
				es = append(es, MakeEdge(src, v))
				src = v
			}
			return ls, es
		}
		var order [][]NodeID
		used := map[Edge]bool{}
		pick := func(c pathcost.Class, diverse bool) {
			var best []NodeID
			var bestCost uint64
			var bestShared int
			for _, h := range paths {
				if diverse && slices.ContainsFunc(order, func(x []NodeID) bool { return slices.Equal(x, h) }) {
					continue
				}
				ls, es := links(h)
				cost := pathcost.Path(ls, c)
				shared := 0
				for _, e := range es {
					if diverse && used[e] {
						shared++
					}
				}
				if best == nil || shared < bestShared || shared == bestShared && (cost < bestCost || cost == bestCost && (len(h) < len(best) || len(h) == len(best) && slices.Compare(h, best) < 0)) {
					best, bestCost, bestShared = h, cost, shared
				}
			}
			if !slices.ContainsFunc(order, func(x []NodeID) bool { return slices.Equal(x, best) }) {
				order = append(order, best)
				_, es := links(best)
				for _, e := range es {
					used[e] = true
				}
			}
		}
		for _, c := range []pathcost.Class{pathcost.Interactive, pathcost.Realtime, pathcost.Bulk} {
			pick(c, false)
		}
		for len(order) < len(paths) {
			pick(pathcost.Interactive, true)
		}
		for n := 1; n <= len(order); n++ {
			tp.Candidates = n
			pt, _ := Compute(in, nil, false)
			if got := hops(set(t, pt, 1, 5)); !slices.EqualFunc(got, order[:n], slices.Equal) {
				t.Fatalf("limit %d: got %v want %v", n, got, order[:n])
			}
		}
	}

}

func TestTimeDomainAndTopologyEquality(t *testing.T) {
	tp := topo(2, Demand{Src: 1, Dst: 2})
	tp.ProbeIntervalMS = 1 << 62
	tp.Switch.Hold = 1 << 40
	if tp.holdMS() != 1<<63-1 || tp.KeepaliveMS() != 1<<63-1 {
		t.Fatal("time wrapped")
	}
	in := Inputs{Topo: tp, Now: -1, Links: LinkTable{}}
	up(in.Links, 1, 2, 1000)
	pt, _ := Compute(in, nil, true)
	if len(pt.BySrc) != 0 {
		t.Fatal("future report used")
	}
	tp.ProbeIntervalMS = 1
	if tp.holdMS() != 1<<40 {
		t.Fatal("hold shortened")
	}
	other := *tp
	other.Cover = map[Edge]Cover{{1, 2}: {}}
	tp.Cover = map[Edge]Cover{{1, 3}: {}}
	if tp.Equal(&other) {
		t.Fatal("zero-valued covers conflated")
	}
	other.Cover = tp.Cover
	other.Nodes = maps.Clone(tp.Nodes)
	n := other.Nodes[1]
	n.Load++
	other.Nodes[1] = n
	if tp.Equal(&other) {
		t.Fatal("load change missed")
	}
}

func TestDemandKeysBelongToSnapshot(t *testing.T) {
	d := Demand{Src: 1, Dst: 2, Via: []NodeID{2}, Allow: []NodeID{1, 2}}
	tp := topo(2, d)
	in := Inputs{Topo: tp, Now: now, Links: LinkTable{}}
	up(in.Links, 1, 2, 100)
	pt, _ := Compute(in, nil, true)
	tp.Demand[0].Via[0] = 3
	tp.Demand[0].Allow[1] = 3
	if _, ok := pt.SetOf(Demand{Src: 1, Dst: 2, Via: []NodeID{2}, Allow: []NodeID{1, 2}}); !ok {
		t.Fatal("snapshot key changed")
	}
	next, dirty := Compute(in, pt, true)
	if _, ok := dirty[1]; !ok || len(next.BySrc[1]) != 0 {
		t.Fatal("source change missed")
	}
}

func TestDefaultIntervalQueries(t *testing.T) {
	tp := topo(2, Demand{Src: 1, Dst: 2})
	tp.ProbeIntervalMS = 0
	in := Inputs{Topo: tp, Now: now + 1, Links: LinkTable{}}
	up(in.Links, 1, 2, 100)
	pt, _ := Compute(in, nil, true)
	ps := set(t, pt, 1, 2)
	l, u, k := in.Link(1, 2)
	c, eu, ek := EdgeCost(in, MakeEdge(1, 2))
	if !u || !k || !eu || !ek || c != ps.Paths[0].CostUs || l != ps.Paths[0].Links[0] || tp.KeepaliveMS() != 5*DefaultProbeIntervalMS || tp.ProbeIntervalMS != 0 {
		t.Fatal("inconsistent default interval")
	}
}
