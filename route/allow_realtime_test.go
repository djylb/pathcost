package route

import (
	"slices"
	"testing"
)

// Allow prunes the graph — every hop, source and
// destination included, must be in it; a demand whose source or
// destination is outside has no path (no fallback), and its set is its
// own (keyed by the allow set).
func TestRouteNodes(t *testing.T) {
	tp := topo(4, Demand{Src: 1, Dst: 3}, Demand{Src: 1, Dst: 3, Allow: []NodeID{1, 3, 4}}, Demand{Src: 1, Dst: 3, Allow: []NodeID{1, 2}})
	lt := LinkTable{}
	meas(lt, 1, 2, LinkMeas{RTTus: 1_000})
	meas(lt, 2, 3, LinkMeas{RTTus: 1_000})
	meas(lt, 1, 4, LinkMeas{RTTus: 9_000})
	meas(lt, 4, 3, LinkMeas{RTTus: 9_000})
	pt, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
	if ps := set(t, pt, 1, 3); !slices.Equal(ps.Paths[0].Hops, []NodeID{2, 3}) {
		t.Fatalf("free: %v", hops(ps))
	}
	ps, ok := pt.SetOf(Demand{Src: 1, Dst: 3, Allow: []NodeID{1, 3, 4}})
	if !ok || slices.ContainsFunc(ps.Paths, func(p Path) bool { return slices.Contains(p.Hops, 2) }) {
		t.Fatalf("through 2 although outside Allow: %v", hops(ps))
	}
	if ps, ok := pt.SetOf(Demand{Src: 1, Dst: 3, Allow: []NodeID{1, 2}}); ok && len(ps.Paths) > 0 {
		t.Fatalf("destination outside Allow: %v", hops(ps))
	}
}

// A realtime flow over a carrier without unreliable datagrams
// weighs loss more; the set carries the realtime-best path and its cost.
func Test6Realtime(t *testing.T) {
	tp := topo(3, Demand{Src: 1, Dst: 3})
	tp.Candidates = 1
	lt := LinkTable{}
	meas(lt, 1, 3, LinkMeas{RTTus: 1_000, LossPPM: 20_000, Carrier: "tls", NoDatagram: true})
	meas(lt, 1, 2, LinkMeas{RTTus: 6_000, Carrier: "quic"})
	meas(lt, 2, 3, LinkMeas{RTTus: 6_000, Carrier: "quic"})
	pt, _ := Compute(Inputs{Topo: tp, Links: lt, Now: now}, nil, true)
	ps := set(t, pt, 1, 3)
	if len(ps.Paths) == 0 || ps.Paths[0].RtCostUs <= ps.Paths[0].CostUs {
		t.Fatalf("the TCP-like direct edge: realtime above interactive: %+v", ps.Paths)
	}
	tp.Candidates = 3
	lt2 := LinkTable{}
	meas(lt2, 1, 3, LinkMeas{RTTus: 3_000, LossPPM: 30_000, Carrier: "tls", NoDatagram: true})
	meas(lt2, 1, 2, LinkMeas{RTTus: 2_500, Carrier: "quic"})
	meas(lt2, 2, 3, LinkMeas{RTTus: 2_500, LossPPM: 30_000, Carrier: "quic"})
	pt, _ = Compute(Inputs{Topo: tp, Links: lt2, Now: now}, nil, true)
	ps = set(t, pt, 1, 3)
	if !slices.ContainsFunc(ps.Paths, func(p Path) bool { return slices.Equal(p.Hops, []NodeID{2, 3}) && p.RtCostUs < ps.Paths[0].RtCostUs }) {
		t.Fatalf("the datagram path is in the set for realtime flows: %+v", ps.Paths)
	}
}
