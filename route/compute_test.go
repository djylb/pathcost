package route

import (
	"fmt"
	"maps"

	"math/rand/v2"
	"slices"
	"testing"

	"github.com/djylb/pathcost"
)

const now = 1_000_000

// topo creates n active transit nodes with IDs from 1 to n and the demands ds.
func topo(n int, ds ...Demand) *Topo {
	t := Topo{Nodes: map[NodeID]Node{}, Cover: map[Edge]Cover{}, Demand: SortDemand(ds)}
	for i := NodeID(1); i <= NodeID(n); i++ {
		t.Nodes[i] = Node{ID: i, Active: true, Transit: true}
	}
	return new(t.Norm())
}

func TestTopoNormReturnsCopy(t *testing.T) {
	input := Topo{MaxHops: -1, Candidates: 2, Switch: pathcost.Hysteresis{GainPct: 101}}
	before := input
	got := input.Norm()
	if !input.Equal(&before) {
		t.Fatal("Norm changed its receiver")
	}
	if got.MaxHops != DefaultMaxHops || got.Candidates != 2 || got.RelayProbe != DefaultRelayProbe ||
		got.ProbeIntervalMS != DefaultProbeIntervalMS || got.Switch != before.Switch.Norm() {
		t.Fatalf("unexpected normalized settings: %+v", got)
	}
}

func TestEdgeEndpoints(t *testing.T) {
	e := MakeEdge(2, 1)
	if e.Side(1) != 0 || e.Side(2) != 1 || e.Other(1) != 2 || e.Other(2) != 1 {
		t.Fatalf("unexpected endpoint order: %v", e)
	}
	t.Run("Side", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("Side accepted a non-endpoint")
			}
		}()
		e.Side(3)
	})
	t.Run("Other", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("Other accepted a non-endpoint")
			}
		}()
		e.Other(3)
	})
}

func TestPathTableEdge(t *testing.T) {
	active, stale, absent := MakeEdge(1, 2), MakeEdge(1, 3), MakeEdge(2, 3)
	lt := LinkTable{stale: {}}
	up(lt, 1, 2, 2000)
	in := Inputs{Topo: topo(3), Links: lt, Now: now}
	pt, _ := Compute(in, nil, true)

	state, ok := pt.Edge(active)
	cost, usable, known := EdgeCost(in, active)
	if !ok || !known || !usable || !state.Usable || !state.Available || state.Cost != cost ||
		state.Directions[active.Side(1)].RTTus != 2000 {
		t.Fatalf("unexpected edge state: %+v, present=%t", state, ok)
	}
	original := state
	state.Directions[active.Side(1)].RTTus++
	if got, _ := pt.Edge(active); got != original {
		t.Fatalf("editing returned state changed the snapshot: %+v", got)
	}
	if got, ok := pt.Edge(stale); !ok || got != (EdgeState{}) {
		t.Fatalf("stale edge: %+v, present=%t", got, ok)
	}
	if got, ok := pt.Edge(absent); ok || got != (EdgeState{}) {
		t.Fatalf("absent edge: %+v, present=%t", got, ok)
	}
	if got, ok := (*PathTable)(nil).Edge(active); ok || got != (EdgeState{}) {
		t.Fatalf("nil table: %+v, present=%t", got, ok)
	}
}

// Up reports a and b up with rtt (µs) from a's side.
func up(lt LinkTable, a, b NodeID, rtt uint32) {
	e := MakeEdge(a, b)
	s := lt[e]
	s[e.Side(a)] = LinkMeas{Up: true, RTTus: rtt, At: now}
	lt[e] = s
}

func down(lt LinkTable, a, b NodeID) {
	e := MakeEdge(a, b)
	s := lt[e]
	s[e.Side(a)] = LinkMeas{Up: false, At: now}
	lt[e] = s
}

func hops(ps PathSet) [][]NodeID {
	var out [][]NodeID
	for _, p := range ps.Paths {
		out = append(out, p.Hops)
	}
	return out
}

func set(t *testing.T, pt *PathTable, src, dst NodeID, via ...NodeID) PathSet {
	t.Helper()
	ps, _ := pt.Set(src, dst, via)
	return ps
}

// Triangle, a–c down: the candidate is over b; a–c back up takes
// the first place again.
func TestTriangle(t *testing.T) {
	tp := topo(3, Demand{Src: 1, Dst: 3})
	lt := LinkTable{}
	up(lt, 1, 2, 1000)
	up(lt, 2, 3, 1000)
	down(lt, 1, 3)
	pt, dirty := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
	if got := hops(set(t, pt, 1, 3)); !slices.EqualFunc(got, [][]NodeID{{2, 3}}, slices.Equal) {
		t.Fatalf("paths %v", got)
	}
	if _, ok := dirty[1]; !ok {
		t.Fatalf("dirty %v", dirty)
	}
	up(lt, 1, 3, 5000)
	pt2, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now, Changed: map[Edge]struct{}{MakeEdge(1, 3): {}}}, pt, false)
	got := hops(set(t, pt2, 1, 3))
	if !slices.EqualFunc(got, [][]NodeID{{2, 3}, {3}}, slices.Equal) {
		t.Fatalf("direct 5 ms vs relay 2 ms: %v", got)
	}
	up(lt, 1, 3, 500)
	pt3, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now, Changed: map[Edge]struct{}{MakeEdge(1, 3): {}}}, pt2, false)
	if got := hops(set(t, pt3, 1, 3)); !slices.EqualFunc(got, [][]NodeID{{2, 3}, {3}}, slices.Equal) || pt3.Recheck == 0 {
		t.Fatalf("direct 0.5 ms before the hold: %v (recheck %d)", got, pt3.Recheck)
	}
	pt3, _ = Compute(Inputs{Topo: tp, Links: lt, Now: pt3.Recheck}, pt3, false)
	if got := hops(set(t, pt3, 1, 3)); !slices.EqualFunc(got, [][]NodeID{{3}, {2, 3}}, slices.Equal) {
		t.Fatalf("direct 0.5 ms after the hold: %v", got)
	}
	if pt3.Gen <= pt2.Gen || pt2.Gen <= pt.Gen {
		t.Fatalf("gens %d %d %d", pt.Gen, pt2.Gen, pt3.Gen)
	}
}

// Two equal shortest paths sharing a bottleneck vs a disjoint longer
// one: the second candidate is the disjoint one.
func TestDiversity(t *testing.T) {
	tp := topo(5, Demand{Src: 1, Dst: 5})
	tp.MaxHops = 3
	lt := LinkTable{}
	up(lt, 1, 2, 100) // bottleneck 1–2
	up(lt, 2, 3, 100)
	up(lt, 2, 4, 100)
	up(lt, 3, 5, 100)
	up(lt, 4, 5, 100)
	up(lt, 1, 5, 1000) // disjoint, longer
	pt, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
	got := set(t, pt, 1, 5)
	if len(got.Paths) < 2 || !slices.Equal(got.Paths[0].Hops, []NodeID{2, 3, 5}) || !slices.Equal(got.Paths[1].Hops, []NodeID{5}) {
		t.Fatalf("paths %v why %v", hops(got), got.Why)
	}
}

// Max_hops bounds the paths; non-transit nodes are never middle
// hops but are endpoints.
func TestHopsAndTransit(t *testing.T) {
	tp := topo(4, Demand{Src: 1, Dst: 4}, Demand{Src: 1, Dst: 2})
	lt := LinkTable{}
	up(lt, 1, 2, 10)
	up(lt, 2, 3, 10)
	up(lt, 3, 4, 10)
	pt, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
	if ps := set(t, pt, 1, 4); len(ps.Paths) != 0 {
		t.Fatalf("3 hops with H=2: %v", hops(ps))
	}
	up(lt, 2, 4, 10)
	n := tp.Nodes[2]
	n.Transit = false
	tp.Nodes[2] = n
	pt, _ = Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
	if ps := set(t, pt, 1, 4); len(ps.Paths) != 0 {
		t.Fatalf("non-transit middle hop: %v", hops(ps))
	}
	if ps := set(t, pt, 1, 2); !slices.EqualFunc(hops(ps), [][]NodeID{{2}}, slices.Equal) {
		t.Fatalf("non-transit endpoint: %v", hops(ps))
	}
}

// Via: the shortest over any via node; the source in via is
// enough; via without transit nodes has no path (never direct).
func TestVia(t *testing.T) {
	tp := topo(4, Demand{Src: 1, Dst: 4, Via: []NodeID{2, 3}}, Demand{Src: 1, Dst: 4, Via: []NodeID{1}})
	lt := LinkTable{}
	up(lt, 1, 4, 10)
	up(lt, 1, 2, 100)
	up(lt, 2, 4, 100)
	up(lt, 1, 3, 50)
	up(lt, 3, 4, 50)
	pt, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
	if ps := set(t, pt, 1, 4, 2, 3); !slices.Equal(ps.Paths[0].Hops, []NodeID{3, 4}) || slices.ContainsFunc(ps.Paths, func(p Path) bool { return len(p.Hops) == 1 }) {
		t.Fatalf("via {2,3}: %v", hops(ps))
	}
	if ps := set(t, pt, 1, 4, 1); !slices.Equal(ps.Paths[0].Hops, []NodeID{4}) {
		t.Fatalf("via {src}: %v", hops(ps))
	}
	for _, id := range []NodeID{2, 3} {
		n := tp.Nodes[id]
		n.Transit = false
		tp.Nodes[id] = n
	}
	pt, _ = Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
	if _, ok := pt.Set(1, 4, []NodeID{2, 3}); ok {
		t.Fatal("via without transit nodes has a path")
	}
}

// Cost rules.
func TestEdgeCost(t *testing.T) {
	tp := topo(2)
	e := MakeEdge(1, 2)
	in := Inputs{Topo: tp, Links: LinkTable{}, Now: now}
	if _, _, known := EdgeCost(in, e); known {
		t.Fatal("no report is known")
	}
	in.Links[e] = [2]LinkMeas{{Up: true, RTTus: 100, At: now}, {Up: true, RTTus: 300, At: now - 1}}
	if c, ok, _ := EdgeCost(in, e); !ok || c != 300 {
		t.Fatalf("larger side: %d %v", c, ok)
	}
	tp.Cover[e] = Cover{BiasUs: 7}
	if c, ok, _ := EdgeCost(in, e); !ok || c != 307 {
		t.Fatalf("cover bias adds to the measurement: %d %v", c, ok)
	}
	in.Links[e] = [2]LinkMeas{{Up: false, At: now}, {Up: true, RTTus: 300, At: now - 1}}
	if _, ok, _ := EdgeCost(in, e); ok {
		t.Fatal("the fresher side is down; a bias does not lift it")
	}
	tp.Cover[e] = Cover{}
	in.Links[e] = [2]LinkMeas{{Up: true, RTTus: 100, LossPPM: 500_000, At: now}}
	if _, ok, _ := EdgeCost(in, e); ok {
		t.Fatal("50 % loss is down")
	}
	in.Links[e] = [2]LinkMeas{{Up: true, RTTus: 100, At: now - 3*tp.KeepaliveMS() - 1}}
	if _, _, known := EdgeCost(in, e); known {
		t.Fatal("stale report is known")
	}
}

// Determinism over map orders.
func TestDeterministic(t *testing.T) {
	var ds []Demand
	for s := NodeID(1); s <= 8; s++ {
		for d := NodeID(1); d <= 8; d++ {
			if s != d {
				ds = append(ds, Demand{Src: s, Dst: d})
			}
		}
	}
	tp := topo(8, ds...)
	lt := LinkTable{}
	r := rand.New(rand.NewPCG(1, 2))
	for a := NodeID(1); a <= 8; a++ {
		for b := a + 1; b <= 8; b++ {
			up(lt, a, b, uint32(100+r.IntN(5)*100)) // many ties
		}
	}
	for range 20 {
		lt2 := LinkTable{}
		maps.Copy(lt2, lt)
		pt, _ := Compute(Inputs{Topo: tp, Links: lt2, Now: now}, nil, true)
		pt.used = nil
		p0, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
		p0.used = nil
		if fmt.Sprint(pt.BySrc, pt.Probe) != fmt.Sprint(p0.BySrc, p0.Probe) {
			t.Fatal("non-deterministic")
		}
	}
}

// Small cost changes refresh published links. A new first path must
// sustain the configured gain for the full hold.
func TestHysteresis(t *testing.T) {
	tp := topo(3, Demand{Src: 1, Dst: 3})
	lt := LinkTable{}
	up(lt, 1, 3, 1000)
	up(lt, 1, 2, 500)
	up(lt, 2, 3, 600)
	pt, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
	if !slices.Equal(set(t, pt, 1, 3).Paths[0].Hops, []NodeID{3}) {
		t.Fatalf("%v", hops(set(t, pt, 1, 3)))
	}
	up(lt, 1, 3, 1050) // 5 %
	ch := map[Edge]struct{}{MakeEdge(1, 3): {}}
	pt2, dirty := Compute(Inputs{Topo: tp, Links: lt, Now: now, Changed: ch}, pt, false)
	if pt2.Gen == pt.Gen || len(dirty) == 0 {
		t.Fatalf("5 %% jitter: gen %d→%d dirty %v", pt.Gen, pt2.Gen, dirty)
	}
	up(lt, 1, 3, 1250) // relay 1100 is 12 % cheaper: kept, nothing pending
	pt3, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now, Changed: ch}, pt2, false)
	if ps := set(t, pt3, 1, 3); !slices.Equal(ps.Paths[0].Hops, []NodeID{3}) || len(pt3.pending) != 0 {
		t.Fatalf("within 20 %%: %v %v", hops(ps), ps.Why)
	}
	up(lt, 1, 3, 1500) // relay 1100 is 27 % cheaper: pending for 3 periods
	hold := int64(tp.Switch.Hold) * tp.ProbeIntervalMS
	pt4, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now, Changed: ch}, pt3, false)
	if ps := set(t, pt4, 1, 3); !slices.Equal(ps.Paths[0].Hops, []NodeID{3}) || pt4.Recheck != now+hold {
		t.Fatalf("27 %% cheaper at once: %v %v recheck %d", hops(ps), ps.Why, pt4.Recheck)
	}
	pt5, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now + hold - 1}, pt4, false)
	if ps := set(t, pt5, 1, 3); !slices.Equal(ps.Paths[0].Hops, []NodeID{3}) {
		t.Fatalf("before the hold: %v", hops(ps))
	}
	// the relay stops being cheaper inside the hold: the wait restarts
	up(lt, 1, 3, 1100)
	pt6, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now + hold - 1, Changed: ch}, pt5, false)
	if len(pt6.pending) != 0 {
		t.Fatalf("pending kept: %v", pt6.pending)
	}
	up(lt, 1, 3, 1500)
	pt7, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now + hold, Changed: ch}, pt6, false)
	if ps := set(t, pt7, 1, 3); !slices.Equal(ps.Paths[0].Hops, []NodeID{3}) {
		t.Fatalf("restarted wait switched early: %v", hops(ps))
	}
	pt8, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now + 2*hold}, pt7, false)
	if ps := set(t, pt8, 1, 3); !slices.Equal(ps.Paths[0].Hops, []NodeID{2, 3}) || len(pt8.pending) != 0 {
		t.Fatalf("after the hold: %v %v", hops(ps), ps.Why)
	}
}

// Incremental against full on random graphs and measurements: the
// same candidate sets, the first path within the switch gain unless a
// switch to a cheaper one waits out the hold.
func TestIncrementalDiff(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 9))
	for round := range 30 {
		n := 6 + r.IntN(4)
		var ds []Demand
		for range 10 {
			s, d := NodeID(1+r.IntN(n)), NodeID(1+r.IntN(n))
			if s != d {
				ds = append(ds, Demand{Src: s, Dst: d})
			}
		}
		tp := topo(n, ds...)
		lt := LinkTable{}
		var edges []Edge
		for a := NodeID(1); a <= NodeID(n); a++ {
			for b := a + 1; b <= NodeID(n); b++ {
				if r.IntN(2) == 0 {
					up(lt, a, b, uint32(100+r.IntN(1000)))
					edges = append(edges, MakeEdge(a, b))
				}
			}
		}
		prev, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
		for range 10 {
			e := edges[r.IntN(len(edges))]
			if r.IntN(3) == 0 {
				down(lt, e.A, e.B)
			} else {
				up(lt, e.A, e.B, uint32(100+r.IntN(1000)))
			}
			inc, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now, Changed: map[Edge]struct{}{e: {}}}, prev, false)
			full, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
			for _, d := range tp.Demand {
				a, _ := inc.Set(d.Src, d.Dst, nil)
				b, _ := full.Set(d.Src, d.Dst, nil)
				if (len(a.Paths) == 0) != (len(b.Paths) == 0) {
					t.Fatalf("round %d %v: inc %v full %v", round, d, hops(a), hops(b))
				}
				if len(a.Paths) == 0 {
					continue
				}
				fresh := Inputs{Topo: tp, Links: lt, Now: now}
				g := buildGraph(fresh)
				current := a.Paths[0].Hops
				ok := g.pathAllowed(d.Src, current, nil) && g.pathUp(d.Src, current, false)
				ac := g.interactivePath(d.Src, current).CostUs
				_, waiting := inc.pending[d.key()]
				if !ok || !waiting && ac*100 > b.Paths[0].CostUs*100*100/(100-tp.Switch.GainPct)+100 {
					t.Fatalf("round %d %v: inc first %v (%d, %v) vs full %v (%d)", round, d, a.Paths[0].Hops, ac, ok, b.Paths[0].Hops, b.Paths[0].CostUs)
				}
			}
			prev = inc
		}
	}
}

// Relay probes start one probe interval after a demand pair went
// unusable, at most relay_probe of them, lowest load first; a pair wanted
// both ways is measured by the lower id.
func TestProbeClosure(t *testing.T) {
	tp := topo(7, Demand{Src: 1, Dst: 2}, Demand{Src: 2, Dst: 1})
	tp.RelayProbe = 2
	for id, l := range map[NodeID]int{3: 5, 4: 1, 5: 2, 6: 9, 7: 0} {
		n := tp.Nodes[id]
		n.Load = l
		tp.Nodes[id] = n
	}
	lt := LinkTable{}
	pt, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
	if !slices.Equal(pt.Probe[1], []NodeID{2}) || len(pt.Probe[2]) != 0 {
		t.Fatalf("probe %v", pt.Probe)
	}
	pt, _ = Compute(Inputs{Topo: tp, Links: lt, Now: now + tp.ProbeIntervalMS}, pt, true)
	if !slices.Equal(pt.Probe[1], []NodeID{2, 4, 7}) || !slices.Equal(pt.Probe[2], []NodeID{4, 7}) || len(pt.Probe[4])+len(pt.Probe[7]) != 0 {
		t.Fatalf("relay probes %v", pt.Probe)
	}
	up(lt, 1, 2, 100)
	pt, _ = Compute(Inputs{Topo: tp, Links: lt, Now: now + tp.ProbeIntervalMS}, pt, true)
	// usable: one relay per pair stays measured, taking turns by window
	//: here the second by load order
	if !slices.Equal(pt.Probe[1], []NodeID{2, 4}) || !slices.Equal(pt.Probe[2], []NodeID{4}) || len(pt.badSince) != 0 {
		t.Fatalf("usable again: %v %v", pt.Probe, pt.badSince)
	}
}

// Full computation budget check: V=64, E=256 full compute.
func BenchmarkCompute64(b *testing.B) {
	r := rand.New(rand.NewPCG(3, 4))
	var ds []Demand
	for s := NodeID(1); s <= 64; s++ {
		for range 8 {
			if d := NodeID(1 + r.IntN(64)); d != s {
				ds = append(ds, Demand{Src: s, Dst: d})
			}
		}
	}
	tp := topo(64, ds...)
	lt := LinkTable{}
	for len(lt) < 256 {
		a, c := NodeID(1+r.IntN(64)), NodeID(1+r.IntN(64))
		if a != c {
			up(lt, a, c, uint32(100+r.IntN(5000)))
		}
	}
	for b.Loop() {
		Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
	}
}

// The relay path coming back after its link quota cleared competes
// against the switch hysteresis. The rates measured on the traffic follow
// the scheduling, here 400 kB/s on 3→2, so 3-2-4 costs about 42 ms
// against about 47 ms of 3-1-4 (1-3 biased 30 ms): not 20 % cheaper, no switch
// ever starts. With 1-3 biased 100 ms the switch waits out the hold and
// takes place.
func TestRelayBackAgainstHysteresis(t *testing.T) {
	meas := func(from NodeID, rtt uint32, out, in uint64) LinkMeas {
		return LinkMeas{Up: true, RTTus: rtt, At: now, RateOutBps: out, RateInBps: in}
	}
	lt := LinkTable{
		MakeEdge(1, 2): {meas(1, 1, 4637943, 0), meas(2, 1, 1000000, 7050249)},
		MakeEdge(1, 3): {meas(1, 1, 81980000, 0), meas(3, 1, 1000000, 131071500)},
		MakeEdge(1, 4): {meas(1, 1, 24967257, 0), meas(4, 9, 1000000, 7566537)},
		MakeEdge(2, 3): {meas(2, 887, 1000000, 400000), meas(3, 887, 400000, 29846720)},
		MakeEdge(2, 4): {meas(2, 1, 90090000, 131071500), meas(4, 164, 65622000, 131071500)},
	}
	down(lt, 3, 4)
	back := func(cover map[Edge]Cover) PathSet {
		tp := topo(4, Demand{Src: 3, Dst: 4})
		tp.Cover = cover
		tp.Exhausted = map[Edge]struct{}{MakeEdge(2, 3): {}}
		pt, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
		if ps := set(t, pt, 3, 4); !slices.Equal(ps.Paths[0].Hops, []NodeID{1, 4}) {
			t.Fatalf("2-3 exhausted: %v", hops(ps))
		}
		tp.Exhausted = nil
		hold := int64(tp.Switch.Hold) * tp.ProbeIntervalMS
		pt, _ = Compute(Inputs{Topo: tp, Links: lt, Now: now}, pt, true)
		pt, _ = Compute(Inputs{Topo: tp, Links: lt, Now: now + hold}, pt, false)
		return set(t, pt, 3, 4)
	}
	measured := back(map[Edge]Cover{MakeEdge(1, 3): {BiasUs: 30000}})
	if !slices.Equal(measured.Paths[0].Hops, []NodeID{1, 4}) || measured.Paths[1].CostUs*100 <= measured.Paths[0].CostUs*(100-20) {
		t.Fatalf("measured costs: switched to %v (%v)", hops(measured), measured.Why)
	}
	fixed := back(map[Edge]Cover{MakeEdge(1, 3): {BiasUs: 100000}})
	if !slices.Equal(fixed.Paths[0].Hops, []NodeID{2, 4}) {
		t.Fatalf("biased: %v (%v)", hops(fixed), fixed.Why)
	}
}
