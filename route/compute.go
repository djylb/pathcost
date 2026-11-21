package route

import (
	"cmp"
	"container/heap"
	"fmt"
	"slices"
	"strconv"

	"github.com/djylb/pathcost"
)

// label is a search state's best path.
type label struct {
	shared int
	cost   uint64
	path   []NodeID // hops after the source
	node   NodeID
	hit    bool
	idx    int // heap index, -1 popped
}

func less(a, b *label) bool {
	if a.shared != b.shared {
		return a.shared < b.shared
	}
	if a.cost != b.cost {
		return a.cost < b.cost
	}
	if len(a.path) != len(b.path) {
		return len(a.path) < len(b.path)
	}
	return slices.Compare(a.path, b.path) < 0
}

type lheap []*label

func (h lheap) Len() int           { return len(h) }
func (h lheap) Less(i, j int) bool { return less(h[i], h[j]) }
func (h lheap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].idx = i; h[j].idx = j }
func (h *lheap) Push(x any)        { l := x.(*label); l.idx = len(*h); *h = append(*h, l) }
func (h *lheap) Pop() any {
	old := *h
	l := old[len(old)-1]
	*h = old[:len(old)-1]
	l.idx = -1
	return l
}

type skey struct {
	node NodeID
	hops int
	hit  bool
}

// shortest is the hop-bounded lexicographic shortest path: weight
// (edges shared with used, cost), ties by fewer hops, then the smaller
// node id sequence. Middle hops must be transit nodes; the path must
// cross via (empty: no constraint; the source or destination in via
// satisfies it). nil when there is none.
func (g *graph) shortest(src, dst NodeID, via []NodeID, used map[Edge]struct{}, cls pathcost.Class) *Path {
	h := g.in.Topo.MaxHops
	inVia := func(n NodeID) bool { return len(via) == 0 || slices.Contains(via, n) }
	best := map[skey]*label{}
	done := map[skey]bool{}
	start := &label{node: src, hit: inVia(src)}
	best[skey{src, 0, start.hit}] = start
	pq := &lheap{start}
	for pq.Len() > 0 {
		u := heap.Pop(pq).(*label)
		k := skey{u.node, len(u.path), u.hit}
		if done[k] {
			continue
		}
		done[k] = true
		if u.node == dst {
			if u.hit {
				return g.costed(src, slices.Clone(u.path))
			}
			continue // never through the destination
		}
		if len(u.path) >= h || u.node != src && !g.transit(u.node) {
			continue
		}
		for _, v := range g.adj[u.node] {
			if v == src || slices.Contains(u.path, v) || v != dst && !g.transit(v) {
				continue
			}
			e := MakeEdge(u.node, v)
			c := g.dcost(u.node, v, cls)
			n := &label{shared: u.shared, cost: u.cost + c, node: v, hit: u.hit || inVia(v) && len(via) > 0}
			if _, ok := used[e]; ok {
				n.shared++
			}
			n.path = append(slices.Clip(u.path), v)
			nk := skey{v, len(n.path), n.hit}
			if done[nk] {
				continue
			}
			if o := best[nk]; o != nil && !less(n, o) {
				continue
			}
			best[nk] = n // a worse label still queued is skipped once nk is done
			heap.Push(pq, n)
		}
	}
	return nil
}

// costed is the path hops from src with its costs of both classes.
func (g *graph) costed(src NodeID, hops []NodeID) *Path {
	ls := g.pathLinks(src, hops)
	return &Path{Hops: hops, CostUs: pathcost.Path(ls, pathcost.Interactive), BulkCostUs: pathcost.Path(ls, pathcost.Bulk),
		RtCostUs: pathcost.Path(ls, pathcost.Realtime), Sum: pathcost.Summary(ls)}
}

// cands are up to K candidates: the shortest for interactive
// flows, then each the cheapest of those sharing the fewest edges with
// the ones picked; the cheapest for bulk flows takes the last place when
// none of them is it (one set serves every class, the user orders it by
// its flow's class).
func (g *graph) cands(src, dst NodeID, via []NodeID) ([]Path, []string) {
	out, why := g.candsOf(src, dst, via)
	if len(out) == 0 {
		return out, why
	}
	// one set serves every class: the cheapest of bulk and of realtime
	// flows take the last places when none of them is it
	for _, x := range []struct {
		c   pathcost.Class
		why string
	}{{pathcost.Bulk, "bulk: cheapest for bulk flows"}, {pathcost.Realtime, "realtime: cheapest for datagram flows"}} {
		b := g.shortest(src, dst, via, nil, x.c)
		if b == nil || slices.ContainsFunc(out, func(o Path) bool { return slices.Equal(o.Hops, b.Hops) }) {
			continue
		}
		if len(out) >= g.in.Topo.Candidates && len(out) > 1 {
			out, why = out[:len(out)-1], why[:len(why)-1]
		}
		out, why = append(out, *b), append(why, x.why)
	}
	return out, why
}

func (g *graph) candsOf(src, dst NodeID, via []NodeID) ([]Path, []string) {
	used := map[Edge]struct{}{}
	var out []Path
	var why []string
	for range g.in.Topo.Candidates {
		p := g.shortest(src, dst, via, used, pathcost.Interactive)
		if p == nil || slices.ContainsFunc(out, func(o Path) bool { return slices.Equal(o.Hops, p.Hops) }) {
			break
		}
		shared := 0
		for _, e := range edgesOf(src, p.Hops) {
			if _, ok := used[e]; ok {
				shared++
			}
			used[e] = struct{}{}
		}
		if len(out) == 0 {
			why = append(why, "shortest")
		} else {
			why = append(why, fmt.Sprintf("diverse shared=%d", shared))
		}
		out = append(out, *p)
	}
	return out, why
}

// viaOK is via without the nodes that cannot be middle hops; their
// demand keeps its key, an empty result has no path (a via constraint
// never falls back to direct).
func (g *graph) viaOK(src, dst NodeID, via []NodeID) ([]NodeID, bool) {
	if len(via) == 0 {
		return nil, true
	}
	var out []NodeID
	for _, v := range via {
		if v == src || v == dst || g.transit(v) {
			out = append(out, v)
		}
	}
	return out, len(out) > 0
}

// Compute is the path table of in: every demand when prev is nil
// or full is set (a structural change), else only the demands whose
// paths cross a changed edge or that end on a changed edge's node;
// the others keep prev's sets. A new first path replaces the old one
// only if 10 % cheaper or the old is unusable; cost moves below 20 %
// keep the set unchanged. It returns the sources whose sets or probe
// list changed.
func Compute(in Inputs, prev *PathTable, full bool) (*PathTable, map[NodeID]struct{}) {
	in.Topo.Norm()
	g := buildGraph(in)
	if prev == nil {
		full = true
	}
	touched := map[NodeID]bool{}
	for e := range in.Changed {
		touched[e.A], touched[e.B] = true, true
	}
	out := &PathTable{BySrc: map[NodeID][]PathSet{}, Used: map[Edge]EdgeState{}, Pending: map[string]Pending{}}
	for e, i := range g.edges {
		out.Used[e] = EdgeState{Cost: i.cost, Usable: i.usable}
	}
	for _, d := range in.Topo.Demand {
		var old *PathSet
		if ps, ok := prev.SetOf(d); ok {
			old = &ps
		}
		key := strconv.FormatInt(d.Src, 10) + ":" + d.Key()
		p, waiting := prev.pending(key)
		due := waiting && in.Now >= p.Since+in.Topo.holdMS()
		if !full && !due && !touched[d.Src] && !touched[d.Dst] && (old == nil || !crosses(d.Src, old, in.Changed)) {
			if old != nil {
				out.BySrc[d.Src] = append(out.BySrc[d.Src], *old)
			}
			if waiting {
				out.Pending[key] = p
				if at := p.Since + in.Topo.holdMS(); out.Recheck == 0 || at < out.Recheck {
					out.Recheck = at
				}
			}
			continue
		}
		if waiting {
			out.Pending[key] = p // pathSet keeps it while the switch still waits
		}
		ps := g.pathSet(d, old, out.Pending, &out.Recheck)
		if p, ok := out.Pending[key]; ok && (len(ps.Paths) == 0 || slices.Equal(ps.Paths[0].Hops, p.Hops) || !stillPending(g, d, old, p)) {
			delete(out.Pending, key) // switched, or no longer cheaper
		}
		if len(ps.Paths) > 0 {
			out.BySrc[d.Src] = append(out.BySrc[d.Src], ps)
		}
	}
	var bad map[Edge]int64
	if prev != nil {
		bad = prev.BadSince
	}
	out.Probe, out.BadSince = g.probe(bad)
	dirty := map[NodeID]struct{}{}
	if prev == nil {
		for s := range out.BySrc {
			dirty[s] = struct{}{}
		}
		for s := range out.Probe {
			dirty[s] = struct{}{}
		}
		out.Gen = 1
		return out, dirty
	}
	for _, m := range []map[NodeID][]PathSet{out.BySrc, prev.BySrc} {
		for s := range m {
			if !slices.EqualFunc(out.BySrc[s], prev.BySrc[s], sameSet) {
				dirty[s] = struct{}{}
			}
		}
	}
	for _, m := range []map[NodeID][]NodeID{out.Probe, prev.Probe} {
		for s := range m {
			if !slices.Equal(out.Probe[s], prev.Probe[s]) {
				dirty[s] = struct{}{}
			}
		}
	}
	out.Gen = prev.Gen
	if len(dirty) > 0 {
		out.Gen++
	}
	return out, dirty
}

// pending is the switch waiting for key in t; t may be nil.
func (t *PathTable) pending(key string) (Pending, bool) {
	if t == nil {
		return Pending{}, false
	}
	p, ok := t.Pending[key]
	return p, ok
}

// stillPending reports that p is still a cheaper first than old's first.
func stillPending(g *graph, d Demand, old *PathSet, p Pending) bool {
	if old == nil || len(old.Paths) == 0 {
		return false
	}
	g.allow = d.Allow
	via, _ := g.viaOK(d.Src, d.Dst, d.Via)
	oc, ok := g.pathUsable(d.Src, old.Paths[0].Hops, via)
	nc, nok := g.pathUsable(d.Src, p.Hops, via)
	return ok && nok && g.in.Topo.Switch.Better(nc, oc)
}

// crosses reports a path of ps over a changed edge.
func crosses(src NodeID, ps *PathSet, changed map[Edge]struct{}) bool {
	for _, p := range ps.Paths {
		for _, e := range edgesOf(src, p.Hops) {
			if _, ok := changed[e]; ok {
				return true
			}
		}
	}
	return false
}

// sameSet compares the sent part of two sets (hops, costs, via).
func sameSet(a, b PathSet) bool {
	return a.Dst == b.Dst && slices.Equal(a.Via, b.Via) && slices.EqualFunc(a.Paths, b.Paths, func(x, y Path) bool {
		return x.CostUs == y.CostUs && x.BulkCostUs == y.BulkCostUs && x.RtCostUs == y.RtCostUs && slices.Equal(x.Hops, y.Hops)
	})
}

// pathSet is the candidates of d with the hysteresis against old
// (Topo.Switch): a cheaper first path takes
// the first place only once it has been cheaper by Topo.Switch.GainPct
// for Topo.Switch.Hold probe intervals (pend holds the wait), or at
// once when the old first is unusable; whatever the kind of path
// (direct, relay), and only new flows follow.
func (g *graph) pathSet(d Demand, old *PathSet, pend map[string]Pending, next *int64) PathSet {
	ps := PathSet{Dst: d.Dst, Via: d.Via, Allow: d.Allow}
	key := strconv.FormatInt(d.Src, 10) + ":" + d.Key()
	g.allow = d.Allow // Allow prunes the graph for this demand
	via, ok := g.viaOK(d.Src, d.Dst, d.Via)
	if !ok || !g.allowed(d.Src) || !g.allowed(d.Dst) {
		return ps
	}
	ps.Paths, ps.Why = g.cands(d.Src, d.Dst, via)
	if old == nil || len(old.Paths) == 0 || len(ps.Paths) == 0 {
		return ps
	}
	oc, ok := g.pathUsable(d.Src, old.Paths[0].Hops, via)
	if !ok || slices.Equal(old.Paths[0].Hops, ps.Paths[0].Hops) {
		return g.settle(ps, old)
	}
	if g.in.Topo.Switch.Better(ps.Paths[0].CostUs, oc) {
		hold := g.in.Topo.holdMS()
		p, waiting := pend[key]
		if !waiting || !slices.Equal(p.Hops, ps.Paths[0].Hops) {
			p = Pending{Hops: ps.Paths[0].Hops, Since: g.in.Now}
		}
		if g.in.Now-p.Since >= hold {
			ps.Why[0] = fmt.Sprintf("shortest: %d%% cheaper than the previous first for %d ms", 100-ps.Paths[0].CostUs*100/max(oc, 1), g.in.Now-p.Since)
			return g.settle(ps, old)
		}
		pend[key] = p
		if at := p.Since + hold; *next == 0 || at < *next {
			*next = at
		}
	}
	// Keep the old order (each cost refreshed), drop the old paths gone
	// unusable, fill up with the new candidates.
	kept := PathSet{Dst: d.Dst, Via: d.Via, Allow: d.Allow}
	for i, p := range old.Paths {
		if _, ok := g.pathUsable(d.Src, p.Hops, via); ok {
			kept.Paths = append(kept.Paths, *g.costed(d.Src, p.Hops))
			w := "kept: previous order"
			if i == 0 {
				w = fmt.Sprintf("kept: new best %d us not %d%% below %d us for %d periods yet", ps.Paths[0].CostUs, g.in.Topo.Switch.GainPct, oc, g.in.Topo.Switch.Hold)
			}
			kept.Why = append(kept.Why, w)
		}
	}
	for i, p := range ps.Paths {
		if len(kept.Paths) >= g.in.Topo.Candidates {
			break
		}
		if !slices.ContainsFunc(kept.Paths, func(o Path) bool { return slices.Equal(o.Hops, p.Hops) }) {
			kept.Paths = append(kept.Paths, p)
			kept.Why = append(kept.Why, ps.Why[i])
		}
	}
	return g.settle(kept, old)
}

// settle returns old itself (unchanged) when ps has its hops in order
// and every cost within Topo.Switch.PushPct of old's (smaller cost
// changes do not change a set).
func (g *graph) settle(ps PathSet, old *PathSet) PathSet {
	if len(ps.Paths) != len(old.Paths) {
		return ps
	}
	for i, p := range ps.Paths {
		o := old.Paths[i]
		if !slices.Equal(p.Hops, o.Hops) {
			return ps
		}
		if sw := g.in.Topo.Switch; sw.Pushed(p.CostUs, o.CostUs) || sw.Pushed(p.BulkCostUs, o.BulkCostUs) {
			return ps
		}
	}
	keep := *old
	keep.Why = ps.Why
	return keep
}

// relayWindow is how many probe intervals one relay of a usable pair is
// measured before the next takes its turn (a probe budget).
const relayWindow = 10

// probe is the probe lists: every demand pair, the pairs to and from
// a via node, the must links; a demand pair not usable for one probe
// interval adds pairs over up to relay_probe transit nodes (lowest load
// first). A pair wanted both ways is probed by the lower id only.
func (g *graph) probe(prevBad map[Edge]int64) (map[NodeID][]NodeID, map[Edge]int64) {
	t := g.in.Topo
	want := map[[2]NodeID]bool{}
	add := func(a, b NodeID) {
		if a != b {
			want[[2]NodeID{a, b}] = true
		}
	}
	var relays []Node
	for _, n := range t.Nodes {
		if n.Active && n.Transit {
			relays = append(relays, n)
		}
	}
	slices.SortFunc(relays, func(a, b Node) int {
		if c := cmp.Compare(a.Load, b.Load); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	bad := map[Edge]int64{}
	pairs := map[[2]NodeID]bool{}
	for _, d := range t.Demand {
		pairs[[2]NodeID{d.Src, d.Dst}] = true
		for _, v := range d.Via {
			if v != d.Src && v != d.Dst {
				pairs[[2]NodeID{d.Src, v}] = true
				pairs[[2]NodeID{v, d.Dst}] = true
			}
		}
	}
	for e, c := range t.Cover {
		if c.Mode == LinkMust {
			pairs[[2]NodeID{e.A, e.B}] = true
		}
	}
	usableRelays := func(s, d NodeID) []Node {
		var out []Node
		for _, r := range relays {
			if r.ID != s && r.ID != d && t.Cover[MakeEdge(s, r.ID)].Mode != LinkForbid && t.Cover[MakeEdge(r.ID, d)].Mode != LinkForbid {
				out = append(out, r)
			}
		}
		return out
	}
	for _, p := range sortedPairs(pairs) {
		s, d := p[0], p[1]
		add(s, d)
		e := MakeEdge(s, d)
		if g.usable(e) {
			// A usable pair still has its relay alternatives measured
			// one relay per pair per relayWindow, taking turns
			// by load order, so a better relay can be found without
			// probing every pair over every node.
			if rs := usableRelays(s, d); len(rs) > 0 {
				r := rs[int((g.in.Now/(relayWindow*t.ProbeIntervalMS))%int64(len(rs)))]
				add(s, r.ID)
				add(r.ID, d)
			}
			continue
		}
		since, ok := prevBad[e]
		if !ok {
			since = g.in.Now
		}
		bad[e] = since
		if g.in.Now-since < t.ProbeIntervalMS {
			continue
		}
		for i, r := range usableRelays(s, d) {
			if i >= t.RelayProbe {
				break
			}
			add(s, r.ID)
			add(r.ID, d)
		}
	}
	out := map[NodeID][]NodeID{}
	for p := range want {
		a, b := p[0], p[1]
		if want[[2]NodeID{b, a}] && b < a {
			continue // both ways: the lower id measures
		}
		if t.Cover[MakeEdge(a, b)].Mode == LinkForbid {
			continue
		}
		out[a] = append(out[a], b)
	}
	for a := range out {
		slices.Sort(out[a])
	}
	return out, bad
}

func sortedPairs(m map[[2]NodeID]bool) [][2]NodeID {
	out := make([][2]NodeID, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b [2]NodeID) int {
		if c := cmp.Compare(a[0], b[0]); c != 0 {
			return c
		}
		return cmp.Compare(a[1], b[1])
	})
	return out
}

// holdMS is the hysteresis hold in ms.
func (t *Topo) holdMS() int64 { return int64(min(t.Switch.Hold, 1<<31)) * t.ProbeIntervalMS }

// Recompute thresholds of ChangedEdge: an edge whose interactive cost
// moved by ChangePct % of the cost it was computed with, and at least
// ChangeFloorUs, is worth a recomputation.
const (
	ChangePct     = 20
	ChangeFloorUs = 5000
)

// ChangedEdge reports that an edge computed as old now is (cost, usable)
// enough to recompute the demands over it (Inputs.Changed): its
// usability changed, or its cost moved max(ChangePct %, ChangeFloorUs).
func ChangedEdge(old EdgeState, cost uint64, usable bool) bool {
	d := int64(cost) - int64(old.Cost)
	if d < 0 {
		d = -d
	}
	return usable != old.Usable || usable && d >= max(int64(old.Cost)*ChangePct/100, ChangeFloorUs)
}
