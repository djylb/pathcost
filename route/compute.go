package route

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"

	"github.com/djylb/pathcost"
)

// A prefix trie excludes alternatives already selected for this demand.
type pathPrefix struct {
	next map[NodeID]*pathPrefix
}

// shortest is the hop-bounded lexicographic shortest path outside selected:
// weight (edges shared with used, cost), ties by fewer hops, then the
// smaller node id sequence. Middle hops must be transit nodes; the path
// must cross via (empty: no constraint; the source or destination in via
// satisfies it). nil when there is none.
func (g *graph) shortest(src, dst NodeID, via []NodeID, used map[Edge]struct{}, selected *pathPrefix, allClasses bool) [3]*Path {
	var winners [3]*Path
	if src == dst {
		return winners
	}
	var costs [3]uint64
	var bestShared int
	var visit func(NodeID, []NodeID, bool, int, *pathPrefix)
	visit = func(node NodeID, hops []NodeID, hit bool, shared int, prefix *pathPrefix) {
		hit = hit || slices.Contains(via, node)
		if node == dst {
			if slices.Equal(hops, g.excluded) || !hit || prefix != nil {
				return
			}
			p := g.interactivePath(src, hops)
			if allClasses {
				p.priceOtherClasses()
			}
			var saved *Path
			for cls, cost := range [...]uint64{pathcost.Interactive: p.CostUs, pathcost.Bulk: p.BulkCostUs, pathcost.Realtime: p.RtCostUs} {
				if cost == 0 { // class omitted from this search
					continue
				}
				best := winners[cls]
				if best == nil || shared < bestShared || shared == bestShared && (cost < costs[cls] || cost == costs[cls] && (len(hops) < len(best.Hops) || len(hops) == len(best.Hops) && slices.Compare(hops, best.Hops) < 0)) {
					if saved == nil {
						p.Hops = slices.Clone(hops)
						saved = new(p)
					}
					winners[cls] = saved
					costs[cls] = cost
				}
			}
			bestShared = shared
			return
		}
		if len(hops) >= g.in.Topo.MaxHops {
			return
		}
		neighbors := g.adj[node]
		if len(hops)+1 == g.in.Topo.MaxHops {
			if !g.usable(MakeEdge(node, dst)) {
				return
			}
			neighbors = []NodeID{dst}
		}
		for _, next := range neighbors {
			if next == src || slices.Contains(hops, next) || next != dst && !g.transit(next) {
				continue
			}
			n := shared
			if _, ok := used[MakeEdge(node, next)]; ok {
				n++
			}
			if winners[pathcost.Interactive] != nil && n > bestShared {
				continue
			}
			var child *pathPrefix
			if prefix != nil {
				child = prefix.next[next]
			}
			visit(next, append(hops, next), hit, n, child)
		}
	}
	visit(src, nil, len(via) == 0, 0, selected)
	if p := winners[pathcost.Interactive]; !allClasses && p != nil {
		p.priceOtherClasses()
	}
	return winners
}

func (g *graph) interactivePath(src NodeID, hops []NodeID) Path {
	ls := g.pathLinks(src, hops)
	return Path{Hops: hops, Links: ls, CostUs: pathcost.Path(ls, pathcost.Interactive)}
}

func (p *Path) priceOtherClasses() {
	p.BulkCostUs = pathcost.Path(p.Links, pathcost.Bulk)
	p.RtCostUs = pathcost.Path(p.Links, pathcost.Realtime)
}

// cands reserves the class winners before filling the remaining slots
// with alternatives that share as few edges as possible with those picked.
func (g *graph) cands(src, dst NodeID, via []NodeID, pinned *Path, winners [3]*Path) ([]Path, []string) {
	limit := g.in.Topo.Candidates
	var out []Path
	var why []string
	if pinned != nil {
		if pinned.BulkCostUs == 0 {
			pinned.priceOtherClasses()
		}
		out = append(out, *pinned)
		why = append(why, "kept: switch threshold or hold pending")
		if limit == 1 {
			return out, why
		}
	}
	for _, x := range []struct {
		cls pathcost.Class
		why string
	}{
		{pathcost.Interactive, "shortest"},
		{pathcost.Realtime, "realtime: cheapest for datagram flows"},
		{pathcost.Bulk, "bulk: cheapest for bulk flows"},
	} {
		if pinned != nil && x.cls == pathcost.Interactive {
			continue
		}
		p := winners[x.cls]
		if p == nil {
			return out, why
		}
		if slices.ContainsFunc(out, func(o Path) bool { return slices.Equal(o.Hops, p.Hops) }) {
			continue
		}
		out, why = append(out, *p), append(why, x.why)
		if len(out) == limit {
			return out, why
		}
	}

	used := map[Edge]struct{}{}
	selected := &pathPrefix{}
	record := func(hops []NodeID) int {
		shared := 0
		from, prefix := src, selected
		for _, hop := range hops {
			e := MakeEdge(from, hop)
			if _, ok := used[e]; ok {
				shared++
			}
			used[e] = struct{}{}
			if prefix.next == nil {
				prefix.next = map[NodeID]*pathPrefix{}
			}
			if prefix.next[hop] == nil {
				prefix.next[hop] = &pathPrefix{}
			}
			from, prefix = hop, prefix.next[hop]
		}
		return shared
	}
	for _, p := range out {
		record(p.Hops)
	}
	for len(out) < limit {
		p := g.shortest(src, dst, via, used, selected, false)[pathcost.Interactive]
		if p == nil {
			break
		}
		shared := record(p.Hops)
		out = append(out, *p)
		why = append(why, fmt.Sprintf("diverse shared=%d", shared))
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

// Compute calculates a read-only snapshot. Pass full after structural changes.
// The second result identifies sources with changed probe lists or unequal
// path sets under [PathSet.Equal]. A diagnostic-only change to Why is ignored.
// A changed effective link may introduce a new path for any demand.
func Compute(in Inputs, prev *PathTable, full bool) (*PathTable, map[NodeID]struct{}) {
	topo := *in.Topo
	topo.Norm()
	in.Topo = &topo
	in.Now = max(in.Now, 0)
	g := buildGraph(in)
	if prev == nil {
		full = true
	}
	out := &PathTable{BySrc: map[NodeID][]PathSet{}, Used: g.edges, Pending: map[string]Pending{}}
	// An interior edge can open a path for any demand. Compare effective
	// directed links so expiration and carrier changes need no caller hint.
	var changed map[Edge]struct{}
	if !full {
		changed = maps.Clone(in.Changed)
		for _, states := range []map[Edge]EdgeState{prev.Used, g.edges} {
			for e := range states {
				if prev.Used[e] != g.edges[e] {
					if changed == nil {
						changed = map[Edge]struct{}{}
					}
					changed[e] = struct{}{}
				}
			}
		}
	}
	for _, d := range in.Topo.Demand {
		var old *PathSet
		if ps, ok := prev.SetOf(d); ok {
			old = &ps
		}
		key := switchKey(d)
		p, waiting := prev.pending(key)
		recovering := p.recovery != nil
		due := waiting && !recovering && in.Now-p.Since >= in.Topo.holdMS()
		g.allow = d.Allow
		rebuild := full || due || g.affected(d, changed) || recovering && len(p.recovery.alternative.Hops) > 0
		if !rebuild && !recovering {
			if old != nil {
				out.BySrc[d.Src] = append(out.BySrc[d.Src], *old)
			}
			if waiting {
				out.wait(key, p, in.Topo.holdMS())
			}
			continue
		}
		via, allowed := g.viaOK(d.Src, d.Dst, d.Via)
		if !allowed || !g.allowed(d.Src) || !g.allowed(d.Dst) {
			continue
		}
		var ps PathSet
		if ps, p, recovering = g.recover(d, via, old, p, rebuild); recovering {
			if p.recovery != nil {
				out.Pending[key] = p
				if q := p.recovery.alternative; len(q.Hops) > 0 {
					out.recheck(q, in.Topo.holdMS())
				}
			}
		} else {
			ps, p = g.pathSet(d, via, old, p)
			if len(p.Hops) > 0 {
				out.wait(key, p, in.Topo.holdMS())
			}
		}
		if len(ps.Paths) > 0 {
			out.BySrc[d.Src] = append(out.BySrc[d.Src], ps)
		}
	}
	var bad map[Edge]int64
	if prev != nil {
		bad = prev.BadSince
	}
	out.Probe, out.BadSince = g.probe(bad, out)
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
			if !slices.EqualFunc(out.BySrc[s], prev.BySrc[s], PathSet.Equal) {
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

// A simple path containing e needs a hop for e and at least one more
// for each endpoint of the demand outside e. Interior vertices must
// be permitted transit nodes even if the edge has just disappeared.
func (g *graph) affected(d Demand, changed map[Edge]struct{}) bool {
edges:
	for e := range changed {
		if !g.allowed(e.A) || !g.allowed(e.B) {
			continue
		}
		hops := 1
		for _, n := range [2]NodeID{e.A, e.B} {
			if n != d.Src && n != d.Dst {
				if !g.transit(n) {
					continue edges
				}
				hops++
			}
		}
		if hops <= g.in.Topo.MaxHops {
			return true
		}
	}
	return false
}

// pending is the switch waiting for key in t; t may be nil.
func (t *PathTable) pending(key string) (Pending, bool) {
	if t == nil {
		return Pending{}, false
	}
	p, ok := t.Pending[key]
	return p, ok
}

func switchKey(d Demand) string { return strconv.FormatInt(d.Src, 10) + ":" + d.Key() }

func (t *PathTable) wait(key string, p Pending, hold int64) {
	t.Pending[key] = p
	t.recheck(p, hold)
}
func (t *PathTable) recheck(p Pending, hold int64) {
	if hold > math.MaxInt64-p.Since {
		return
	}
	if at := p.Since + hold; t.Recheck == 0 || at < t.Recheck {
		t.Recheck = at
	}
}

// Equal compares published paths, constraints and selection policy. Why is diagnostic.
func (s PathSet) Equal(other PathSet) bool {
	return cmpSet(s, other) == 0 && s.PreferFirst == other.PreferFirst && slices.EqualFunc(s.Paths, other.Paths, func(x, y Path) bool {
		return slices.Equal(x.Links, y.Links) && x.CostUs == y.CostUs && x.BulkCostUs == y.BulkCostUs && x.RtCostUs == y.RtCostUs && slices.Equal(x.Hops, y.Hops)
	})
}

func (g *graph) winners(d Demand, via []NodeID) [3]*Path {
	return g.shortest(d.Src, d.Dst, via, nil, nil, g.in.Topo.Candidates > 1)
}
func (g *graph) candidates(d Demand, via []NodeID, pinned *Path, winners [3]*Path) PathSet {
	paths, why := g.cands(d.Src, d.Dst, via, pinned, winners)
	return PathSet{Dst: d.Dst, Via: slices.Clone(d.Via), Allow: slices.Clone(d.Allow), Paths: paths, Why: why}
}

// Reuse the search result when the retained path also won a class.
func (g *graph) retained(src NodeID, hops []NodeID, winners [3]*Path) *Path {
	for _, p := range winners {
		if p != nil && slices.Equal(p.Hops, hops) {
			return p
		}
	}
	return new(g.interactivePath(src, hops))
}

// pathSet applies the cost hysteresis to ordinary path changes.
func (g *graph) pathSet(d Demand, via []NodeID, old *PathSet, p Pending) (PathSet, Pending) {
	winners := g.winners(d, via)
	build := func(pinned *Path) PathSet { return g.candidates(d, via, pinned, winners) }
	best := winners[pathcost.Interactive]
	if old == nil {
		return build(nil), Pending{}
	}
	current := old.Paths[0].Hops
	if !g.pathAllowed(d.Src, current, via) || !g.pathUp(d.Src, current, old.PreferFirst) {
		return build(nil), Pending{}
	}
	if best == nil {
		ps := build(g.retained(d.Src, current, winners))
		ps.Why[0], ps.PreferFirst = "kept: previous path still available", true
		return ps, Pending{}
	}
	if slices.Equal(current, best.Hops) {
		ps := build(nil)
		ps.PreferFirst = old.PreferFirst
		return ps, Pending{}
	}
	pinned := g.retained(d.Src, current, winners)
	oc := pinned.CostUs
	var pending Pending
	if g.in.Topo.Switch.Better(best.CostUs, oc) {
		if !slices.Equal(p.Hops, best.Hops) {
			p = Pending{Hops: best.Hops, Since: g.in.Now}
		}
		if g.in.Now-p.Since >= g.in.Topo.holdMS() {
			ps := build(nil)
			ps.Why[0] = fmt.Sprintf("shortest: %d%% cheaper than the previous first for %d ms", 100-best.CostUs*100/max(oc, 1), g.in.Now-p.Since)
			return ps, Pending{}
		}
		pending = p
	}
	ps := build(pinned)
	ps.PreferFirst = old.PreferFirst
	return ps, pending
}

// relayWindow is how many probe intervals one relay of a usable pair is
// measured before the next takes its turn (a probe budget).
const relayWindow = 10

// probe is the probe lists: every demand pair, the pairs to and from
// a via node, the must links; a demand pair not usable for one probe
// interval adds pairs over up to relay_probe transit nodes (lowest load
// first). A pair wanted both ways is probed by the lower id only.
func (g *graph) probe(prevBad map[Edge]int64, paths *PathTable) (map[NodeID][]NodeID, map[Edge]int64) {
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
	usableRelays := func(s, d NodeID) []Node {
		var out []Node
		for _, r := range relays {
			if g.allowed(r.ID) && r.ID != s && r.ID != d && t.Cover[MakeEdge(s, r.ID)].Mode != LinkForbid && t.Cover[MakeEdge(r.ID, d)].Mode != LinkForbid {
				out = append(out, r)
			}
		}
		return out
	}
	plan := func(s, d NodeID) {
		if s == d || !g.allowed(s) || !g.allowed(d) || !t.Nodes[s].Active || !t.Nodes[d].Active {
			return
		}
		add(s, d)
		e := MakeEdge(s, d)
		if g.usable(e) {
			// A usable pair still has its relay alternatives measured
			// one relay per pair per relayWindow, taking turns
			// by load order, so a better relay can be found without
			// probing every pair over every node.
			if rs := usableRelays(s, d); len(rs) > 0 {
				r := rs[int((g.in.Now/t.ProbeIntervalMS/relayWindow)%int64(len(rs)))]
				add(s, r.ID)
				add(r.ID, d)
			}
			return
		}
		since, ok := prevBad[e]
		if !ok {
			since = g.in.Now
		}
		bad[e] = since
		if g.in.Now-since < t.ProbeIntervalMS {
			return
		}
		for i, r := range usableRelays(s, d) {
			if i >= t.RelayProbe {
				break
			}
			add(s, r.ID)
			add(r.ID, d)
		}
	}
	for _, d := range t.Demand {
		g.allow = d.Allow
		if !g.allowed(d.Src) || !g.allowed(d.Dst) || !t.Nodes[d.Src].Active || !t.Nodes[d.Dst].Active || d.Src == d.Dst {
			continue
		}
		vs, ok := g.viaOK(d.Src, d.Dst, d.Via)
		if !ok {
			continue
		}
		plan(d.Src, d.Dst)
		for _, v := range vs {
			if v != d.Src && v != d.Dst {
				plan(d.Src, v)
				plan(v, d.Dst)
			}
		}
	}
	g.allow = nil
	for e, c := range t.Cover {
		if c.Mode == LinkMust {
			plan(e.A, e.B)
		}
	}
	// Every hop of both paths needs comparable RTT while recovering.
	// Keep these pairs in the list even when ordinary relay probes rotate.
	for _, d := range t.Demand {
		p := paths.Pending[switchKey(d)]
		if p.recovery == nil {
			continue
		}
		addPath := func(hops []NodeID) {
			src := d.Src
			for _, n := range hops {
				add(src, n)
				src = n
			}
		}
		addPath(p.Hops)
		if ps, ok := paths.SetOf(d); ok {
			addPath(ps.Paths[0].Hops)
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

// holdMS is the hysteresis hold in ms.
func (t *Topo) holdMS() int64 {
	if t.Switch.Hold > uint64(math.MaxInt64/t.ProbeIntervalMS) {
		return math.MaxInt64
	}
	return int64(t.Switch.Hold) * t.ProbeIntervalMS
}
