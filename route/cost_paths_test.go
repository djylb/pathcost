package route

import (
	"cmp"
	"maps"
	"slices"
	"testing"

	"github.com/djylb/pathcost"
)

func meas(lt LinkTable, a, b NodeID, m LinkMeas) {
	e := MakeEdge(a, b)
	s := lt[e]
	m.Up, m.At, m.From = true, now, a
	s[Side(e, a)] = m
	lt[e] = s
}

// A relay beats a direct edge on loss for both
// classes; the direct edge has no head start.
func TestRelayBeatsLossyDirect(t *testing.T) {
	tp := topo(3, Demand{Src: 1, Dst: 3})
	lt := LinkTable{}
	meas(lt, 1, 3, LinkMeas{RTTus: 10_000, LossPPM: 20_000})
	meas(lt, 1, 2, LinkMeas{RTTus: 6_000})
	meas(lt, 2, 3, LinkMeas{RTTus: 6_000})
	pt, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
	ps := set(t, pt, 1, 3)
	if !slices.Equal(ps.Paths[0].Hops, []NodeID{2, 3}) {
		t.Fatalf("lossy direct first: %v %v", hops(ps), ps.Why)
	}
	for _, p := range ps.Paths {
		if slices.Equal(p.Hops, []NodeID{3}) && (p.CostUs <= ps.Paths[0].CostUs || p.BulkCostUs <= ps.Paths[0].BulkCostUs) {
			t.Fatalf("costs %+v", ps.Paths)
		}
	}
}

// The class decides between a near narrow direct edge and a
// wider relay: interactive flows keep the direct edge first, the set
// also holds the bulk-best relay with the lower bulk cost (nodes order
// by their flow's class), the per-direction rate taken from the sender's
// outbound or the receiver's inbound report.
func TestClassOrders(t *testing.T) {
	tp := topo(3, Demand{Src: 1, Dst: 3})
	lt := LinkTable{}
	meas(lt, 1, 3, LinkMeas{RTTus: 5_000, RateOutBps: 20e6})
	meas(lt, 1, 2, LinkMeas{RTTus: 4_000, RateOutBps: 100e6})
	meas(lt, 3, 2, LinkMeas{RTTus: 4_000, RateInBps: 100e6}) // 2→3 as 3 receives it
	tp.Candidates = 2
	pt, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
	ps := set(t, pt, 1, 3)
	if len(ps.Paths) != 2 || !slices.Equal(ps.Paths[0].Hops, []NodeID{3}) {
		t.Fatalf("interactive first: %v %v", hops(ps), ps.Why)
	}
	var direct, relay Path
	for _, p := range ps.Paths {
		if len(p.Hops) == 1 {
			direct = p
		} else {
			relay = p
		}
	}
	if relay.Hops == nil || relay.BulkCostUs >= direct.BulkCostUs || relay.CostUs <= direct.CostUs {
		t.Fatalf("bulk: direct %+v relay %+v", direct, relay)
	}
	// the reverse direction 3→1 does not count for 1→3: a slow 3→1 rate
	// changes nothing
	meas(lt, 3, 1, LinkMeas{RTTus: 5_000, RateOutBps: 1})
	pt2, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
	if p := set(t, pt2, 1, 3).Paths; !slices.Equal(p[0].Hops, []NodeID{3}) {
		t.Fatalf("reverse direction counted: %+v", p)
	}
}

func TestCandidateClassWinners(t *testing.T) {
	tp := topo(6, Demand{Src: 1, Dst: 6})
	lt := LinkTable{}
	// The direct path wins for interactive flows, relay 5 for realtime,
	// and relay 4 for bulk. Relays 2 and 3 rank ahead of both class
	// winners by interactive cost.
	lossy := LinkMeas{RTTus: 1_000, LossPPM: 20_000, NoDatagram: true}
	meas(lt, 1, 6, lossy)
	meas(lt, 1, 2, lossy)
	up(lt, 2, 6, 100)
	meas(lt, 1, 3, lossy)
	up(lt, 3, 6, 200)
	up(lt, 1, 4, 2_500)
	up(lt, 4, 6, 2_500)
	wide := LinkMeas{RTTus: 1_600, RateOutBps: 100e6}
	meas(lt, 1, 5, wide)
	meas(lt, 5, 6, wide)

	for _, tc := range []struct {
		name  string
		limit int
		want  [][]NodeID
	}{
		{"default", 0, [][]NodeID{{6}, {5, 6}, {4, 6}}},
		{"one", 1, [][]NodeID{{6}}},
		{"two prefer realtime over bulk", 2, [][]NodeID{{6}, {5, 6}}},
		{"spare capacity", 4, [][]NodeID{{6}, {5, 6}, {4, 6}, {2, 6}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			topo := *tp
			topo.Candidates = tc.limit
			pt, _ := Compute(Inputs{Topo: &topo, Links: lt, Now: now}, nil, true)
			ps := set(t, pt, 1, 6)
			if got := hops(ps); !slices.EqualFunc(got, tc.want, slices.Equal) {
				t.Fatalf("paths %v (%v), want %v", got, ps.Why, tc.want)
			}
		})
	}

	t.Run("reduced while holding first", func(t *testing.T) {
		prev, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
		tp.Candidates = 1
		updated := maps.Clone(lt)
		up(updated, 1, 5, 500)
		up(updated, 5, 6, 500)
		pt, _ := Compute(Inputs{Topo: tp, Links: updated, Now: now}, prev, true)
		if got := hops(set(t, pt, 1, 6)); !slices.EqualFunc(got, [][]NodeID{{6}}, slices.Equal) || pt.Recheck == 0 {
			t.Fatalf("paths %v, recheck %d, want the held first path within the new limit", got, pt.Recheck)
		}
	})
}

func TestCandidateDiversityAfterClassWinners(t *testing.T) {
	tp := topo(5, Demand{Src: 1, Dst: 5})
	tp.MaxHops, tp.Candidates = 3, 4
	lt := LinkTable{}
	meas(lt, 1, 5, LinkMeas{RTTus: 1_000, RateOutBps: 20e6})
	wide := LinkMeas{RTTus: 4_000, RateOutBps: 100e6}
	meas(lt, 1, 2, wide)
	meas(lt, 2, 5, wide)
	meas(lt, 2, 3, wide)
	meas(lt, 3, 5, wide)
	meas(lt, 1, 4, LinkMeas{RTTus: 20_000, RateOutBps: 20e6})
	meas(lt, 4, 5, LinkMeas{RTTus: 20_000, RateOutBps: 20e6})
	pt, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
	ps := set(t, pt, 1, 5)
	// Interactive and realtime share the direct path. The disjoint
	// alternative precedes the cheaper one that shares the bulk path's first edge.
	want := [][]NodeID{{5}, {2, 5}, {4, 5}, {2, 3, 5}}
	if got := hops(ps); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("paths %v (%v), want %v", got, ps.Why, want)
	}
	if want := []string{"shortest", "bulk: cheapest for bulk flows", "diverse shared=0", "diverse shared=1"}; !slices.Equal(ps.Why, want) {
		t.Fatalf("reasons %v, want %v", ps.Why, want)
	}
}

// A relay whose uplink has a unit price is avoided by bulk
// flows while an unpriced path exists, even a slower one (the fee is per
// byte: interactive flows, with their small reference transfer, barely
// pay it), and used when it is the only path; a spent quota is used only
// without any other path, whatever the class.
func TestFeeAvoided(t *testing.T) {
	tp := topo(4, Demand{Src: 1, Dst: 3})
	n2 := tp.Nodes[2]
	n2.Fees = map[string]UplinkFee{"": {In: pathcost.FeeOf(0, 10, 0, false), Out: pathcost.FeeOf(0, 10, 0, false)}}
	tp.Nodes[2] = n2
	lt := LinkTable{}
	meas(lt, 1, 2, LinkMeas{RTTus: 1_000})
	meas(lt, 2, 3, LinkMeas{RTTus: 1_000})
	meas(lt, 1, 4, LinkMeas{RTTus: 1_800})
	meas(lt, 4, 3, LinkMeas{RTTus: 1_800})
	pt, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
	ps := set(t, pt, 1, 3)
	best := slices.MinFunc(ps.Paths, func(a, b Path) int { return cmp.Compare(a.BulkCostUs, b.BulkCostUs) })
	if !slices.Equal(best.Hops, []NodeID{4, 3}) || !slices.Equal(ps.Paths[0].Hops, []NodeID{2, 3}) {
		t.Fatalf("bulk avoids the priced relay, interactive takes the faster one: %+v", ps.Paths)
	}
	down(lt, 1, 4)
	pt, _ = Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
	if ps := set(t, pt, 1, 3); len(ps.Paths) == 0 || !slices.Equal(ps.Paths[0].Hops, []NodeID{2, 3}) {
		t.Fatalf("priced not used when needed: %v", hops(ps))
	}
	// spent quota: a 10× slower path wins; alone it is still used
	n2.Fees = map[string]UplinkFee{"": {In: pathcost.Fee{Exhausted: true}, Out: pathcost.Fee{Exhausted: true}}}
	tp.Nodes[2] = n2
	meas(lt, 1, 4, LinkMeas{RTTus: 18_000})
	pt, _ = Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
	if ps := set(t, pt, 1, 3); !slices.Equal(ps.Paths[0].Hops, []NodeID{4, 3}) {
		t.Fatalf("exhausted first: %v", hops(ps))
	}
	down(lt, 1, 4)
	pt, _ = Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
	if ps := set(t, pt, 1, 3); len(ps.Paths) == 0 || !slices.Equal(ps.Paths[0].Hops, []NodeID{2, 3}) {
		t.Fatalf("exhausted alone not used: %v", hops(ps))
	}
}

// The fee is paid at both ends — the sender's uplink
// for what it sends, the receiver's for what it receives — on the
// uplink of the carrier the dialling end dialled (the accepting end) and
// the default uplink on the dialling end. Node and pair biases add.
func TestFeeBothEnds(t *testing.T) {
	tp := topo(2, Demand{Src: 1, Dst: 2})
	f := func(in, out uint64) UplinkFee {
		return UplinkFee{In: pathcost.FeeOf(1, in, 0, false), Out: pathcost.FeeOf(1, out, 0, false)}
	}
	n1, n2 := tp.Nodes[1], tp.Nodes[2]
	n1.Fees = map[string]UplinkFee{"": f(1, 2), "b": f(100, 200)}
	n2.Fees = map[string]UplinkFee{"": f(3, 4), "b": f(300, 400)}
	n2.BiasUs = 70
	tp.Nodes[1], tp.Nodes[2] = n1, n2
	tp.Cover[MakeEdge(1, 2)] = Cover{BiasUs: 5}
	lt := LinkTable{}
	meas(lt, 1, 2, LinkMeas{RTTus: 1_000})
	in := Inputs{Topo: tp, Links: lt, Now: now}
	l, ok, _ := in.Link(1, 2)
	if !ok || l.Fee.UsPerMiB != 2+3 || l.BiasUs != 75 {
		t.Fatalf("defaults: 1 sends (2) + 2 receives (3), bias 5+70: %+v", l)
	}
	// 1 dialled 2's carrier on uplink b: 2's end is b
	meas(lt, 1, 2, LinkMeas{RTTus: 1_000, Uplink: "b"})
	if l, _, _ = in.Link(1, 2); l.Fee.UsPerMiB != 2+300 {
		t.Fatalf("1→2 over 2's uplink b: %+v", l)
	}
	if l, _, _ = in.Link(2, 1); l.Fee.UsPerMiB != 400+1 {
		t.Fatalf("2→1 from 2's uplink b: %+v", l)
	}
}
