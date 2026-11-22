package route

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/djylb/pathcost"
)

// NodeID is a node id. Ids order the tie-breaks, so equal inputs give
// equal outputs.
type NodeID = int64

// Edge is an unordered node pair, A<B.
type Edge struct{ A, B NodeID }

// MakeEdge is the edge of a and b in either order.
func MakeEdge(a, b NodeID) Edge {
	if a > b {
		a, b = b, a
	}
	return Edge{a, b}
}

// Other is the end of e that is not n.
func (e Edge) Other(n NodeID) NodeID {
	if e.A == n {
		return e.B
	}
	return e.A
}

func (e Edge) String() string {
	return strconv.FormatInt(e.A, 10) + "-" + strconv.FormatInt(e.B, 10)
}

// LinkMeas is one end's report of a link.
type LinkMeas struct {
	Up      bool
	RTTus   uint32 // smoothed by the reporter, queueing taken out
	LossPPM uint32
	At      int64  // the receive time, monotonic ms; 0: none
	From    NodeID // the reporting node
	// ProbeAt and ProbeRTTus are a round-trip observation with queueing,
	// separate from the cost estimate. ProbeAt uses the same clock as
	// Inputs.Now and stays fixed on refresh. Zero RTT at a nonzero
	// ProbeAt records a failed probe.
	ProbeAt    int64
	ProbeRTTus uint32
	// Carrier names the transport the measurement used ("" unknown);
	// diagnostics only, never a cost input.
	Carrier string
	// NoDatagram: that transport has no unreliable datagrams (realtime
	// flows weigh its loss more, pathcost.RealtimeLossFactor).
	NoDatagram bool
	// RateOutBps / RateInBps are the reporter's available rate towards
	// the peer / from it (bytes/s, 0 unknown).
	RateOutBps, RateInBps uint64
	// QueueUs is the measured queueing delay (the congestion term).
	QueueUs uint32
	// Uplink is the uplink of the peer the reporter dialled: the peer
	// end's uplink; "" when the reporter accepted (or dialled the peer's
	// default). The dialling end's own uplink is its default ("").
	Uplink string
}

// LinkTable holds both ends' reports per edge: [0] from A, [1] from B.
type LinkTable map[Edge][2]LinkMeas

// Side is the index of n's report on e.
func Side(e Edge, n NodeID) int {
	if n == e.A {
		return 0
	}
	return 1
}

// UplinkFee is the fee of an uplink end for the bytes it sends (Out) and
// receives (In).
type UplinkFee struct{ In, Out pathcost.Fee }

// Node is one node of the topology.
type Node struct {
	ID NodeID
	// Load orders relay probe picks (lower first), such as a session count.
	Load int
	// BiasUs is added to every edge into the node.
	BiasUs int64
	// Fees are the node's uplink ends by name, "" the default uplink; an
	// uplink not listed costs nothing.
	Fees map[string]UplinkFee
	// Active: only active nodes are vertices.
	Active bool
	// Transit: the node may be a middle hop (only active ones are).
	Transit bool
}

// fee is the node end's fee of uplink name for bytes it sends (out) or
// receives.
func (n Node) fee(name string, out bool) pathcost.Fee {
	f := n.Fees[name]
	if out {
		return f.Out
	}
	return f.In
}

// LinkMode is a node pair's constraint.
type LinkMode uint8

// Link modes. The zero value is LinkAuto.
const (
	// LinkAuto: the edge counts when measured usable.
	LinkAuto LinkMode = iota
	// LinkMust: the pair is always measured (probed), even without a
	// demand over it.
	LinkMust
	// LinkForbid: the pair is never an edge and never probed.
	LinkForbid
)

// Cover is a node pair's override: the mode and the bias added to the
// pair's link cost.
type Cover struct {
	Mode   LinkMode
	BiasUs int64
}

// Demand is one (source, destination, via) to compute paths for; Via is
// the set of nodes one of which the path must cross (the source or
// destination in it satisfies it), ascending.
type Demand struct {
	Src, Dst NodeID
	Via      []NodeID
	// Allow is the set of nodes the path may use, ascending: every hop,
	// the source and destination included, is one of them; nil is no
	// limit.
	Allow []NodeID
}

// Key is the (destination, via, allow) key of d within its source.
func (d Demand) Key() string { return pairKey(d.Dst, d.Via) + allowKey(d.Allow) }

func allowKey(allow []NodeID) string {
	var b strings.Builder
	for i, v := range allow {
		if i == 0 {
			b.WriteByte('a')
		} else {
			b.WriteByte('.')
		}
		b.WriteString(strconv.FormatInt(v, 10))
	}
	return b.String()
}

func pairKey(dst NodeID, via []NodeID) string {
	var b strings.Builder
	b.WriteString(strconv.FormatInt(dst, 10))
	for i, v := range via {
		if i == 0 {
			b.WriteByte('v')
		} else {
			b.WriteByte('.')
		}
		b.WriteString(strconv.FormatInt(v, 10))
	}
	return b.String()
}

// Topo is the structural input: the vertices, the pair overrides, the
// demand and the settings. A change of any of it recomputes every
// demand. Zero settings take the defaults (Norm).
type Topo struct {
	Nodes  map[NodeID]Node
	Cover  map[Edge]Cover
	Demand []Demand // sorted by (Src, Dst, Via, Allow), unique (SortDemand)
	// MaxHops bounds the edges of a path. Candidates caps the paths per
	// demand. Fresh selection reserves distinct best paths in interactive,
	// realtime, bulk order before adding edge-diverse alternatives.
	// RelayProbe is how many relays an unusable demand pair is probed over.
	// ProbeIntervalMS is the measurement period.
	MaxHops, Candidates, RelayProbe int
	ProbeIntervalMS                 int64
	// Switch is the hysteresis of a first path's change (Hold in
	// measurement periods).
	Switch pathcost.Hysteresis
	// Exhausted are the edges whose pair quota is used up: each costs
	// pathcost.Exhausted more, so new flows take them only without another
	// path.
	Exhausted map[Edge]struct{}
}

// Defaults of Topo.
const (
	DefaultMaxHops         = 2
	DefaultCandidates      = 3
	DefaultRelayProbe      = 4
	DefaultProbeIntervalMS = 60_000
)

// Norm fills the zero (or negative) settings with the defaults.
func (t *Topo) Norm() {
	if t.MaxHops <= 0 {
		t.MaxHops = DefaultMaxHops
	}
	if t.Candidates <= 0 {
		t.Candidates = DefaultCandidates
	}
	if t.RelayProbe <= 0 {
		t.RelayProbe = DefaultRelayProbe
	}
	if t.ProbeIntervalMS <= 0 {
		t.ProbeIntervalMS = DefaultProbeIntervalMS
	}
	t.Switch = t.Switch.Norm()
}

// KeepaliveMS is the longest a report stays without a refresh (5 ×
// the probe interval); a report older than 3 × it is not fresh.
func (t *Topo) KeepaliveMS() int64 { return 5 * t.ProbeIntervalMS }

// Equal reports the same structure (a full recompute is not needed).
func (t *Topo) Equal(o *Topo) bool {
	if t == nil || o == nil {
		return t == o
	}
	if t.MaxHops != o.MaxHops || t.Candidates != o.Candidates || t.RelayProbe != o.RelayProbe || t.ProbeIntervalMS != o.ProbeIntervalMS ||
		t.Switch != o.Switch ||
		len(t.Nodes) != len(o.Nodes) || len(t.Cover) != len(o.Cover) || len(t.Demand) != len(o.Demand) ||
		len(t.Exhausted) != len(o.Exhausted) {
		return false
	}
	for e := range t.Exhausted {
		if _, ok := o.Exhausted[e]; !ok {
			return false
		}
	}
	for id, n := range t.Nodes {
		m, ok := o.Nodes[id]
		if !ok || n.Active != m.Active || n.Transit != m.Transit || n.BiasUs != m.BiasUs || !maps.Equal(n.Fees, m.Fees) {
			return false
		}
	}
	for e, c := range t.Cover {
		if o.Cover[e] != c {
			return false
		}
	}
	for i, d := range t.Demand {
		x := o.Demand[i]
		if d.Src != x.Src || d.Dst != x.Dst || !slices.Equal(d.Via, x.Via) || !slices.Equal(d.Allow, x.Allow) {
			return false
		}
	}
	return true
}

// SortDemand sorts and dedups ds.
func SortDemand(ds []Demand) []Demand {
	slices.SortFunc(ds, func(a, b Demand) int {
		if c := cmp.Compare(a.Src, b.Src); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Dst, b.Dst); c != 0 {
			return c
		}
		if c := slices.Compare(a.Via, b.Via); c != 0 {
			return c
		}
		return slices.Compare(a.Allow, b.Allow)
	})
	return slices.CompactFunc(ds, func(a, b Demand) bool {
		return a.Src == b.Src && a.Dst == b.Dst && slices.Equal(a.Via, b.Via) && slices.Equal(a.Allow, b.Allow)
	})
}

// Inputs are one computation's inputs.
type Inputs struct {
	Topo  *Topo
	Links LinkTable
	Now   int64 // monotonic ms
	// Changed are the edges whose usability or cost crossed the recompute
	// threshold since prev (ChangedEdge); nil with a prev of the same Topo is
	// "nothing changed". Ignored without prev or when the Topo changed.
	Changed map[Edge]struct{}
}

// Path is one candidate: the hops after the source, the last the
// destination (direct = [dst]), its costs for the flow classes and the
// path as one link (pathcost.Summary: a caller joins it with a further
// hop).
type Path struct {
	Hops       []NodeID
	CostUs     uint64
	BulkCostUs uint64
	RtCostUs   uint64
	Sum        pathcost.Link
}

// PathSet is the candidates of one demand in pick order.
type PathSet struct {
	Dst   NodeID
	Via   []NodeID
	Allow []NodeID
	Paths []Path
	// PreferFirst applies the first path to all flow classes after a
	// failure, until an ordinary cost-based switch replaces it.
	PreferFirst bool
	// Why gives one diagnostic per path in the same order. Examples include
	// "shortest", "realtime: cheapest for datagram flows", "bulk: cheapest
	// for bulk flows", "diverse shared=N", and "kept: …".
	Why []string
}

// PathTable is the output: per source its sets by (Dst, Via), the peers
// each source probes, and per-edge state the next computation carries.
type PathTable struct {
	BySrc map[NodeID][]PathSet
	Probe map[NodeID][]NodeID
	// Gen grows when BySrc or Probe changed.
	Gen uint64
	// BadSince is when an edge of the demand closure was last seen
	// usable→not (or first wanted); the relay probes start one probe
	// interval later.
	BadSince map[Edge]int64
	// Used are the edge costs this table was computed with (ChangedEdge
	// compares against them).
	Used map[Edge]EdgeState
	// Pending are the switches waiting out the hysteresis by (src,
	// demand key): either a cheaper path or the path that failed.
	Pending map[string]Pending
	// Recheck is the earliest time (ms) a pending switch is due; 0: none.
	// The caller recomputes then.
	Recheck int64
}

// Pending is a first path waiting to replace the current one. Since is
// when it became cheaper, or when the failed path was last seen down.
type Pending struct {
	Hops     []NodeID
	Since    int64
	recovery *recovery
}

// EdgeState is an edge as one computation saw it.
type EdgeState struct {
	Cost      uint64
	Usable    bool
	Available bool // the latest fresh report says Up
}

// Set returns src's set for (dst, via) without an Allow limit.
func (t *PathTable) Set(src, dst NodeID, via []NodeID) (PathSet, bool) {
	return t.SetOf(Demand{Src: src, Dst: dst, Via: via})
}

// SetOf returns the set of demand d.
func (t *PathTable) SetOf(d Demand) (PathSet, bool) {
	if t == nil {
		return PathSet{}, false
	}
	ss := t.BySrc[d.Src]
	i, ok := slices.BinarySearchFunc(ss, PathSet{Dst: d.Dst, Via: d.Via, Allow: d.Allow}, cmpSet)
	if !ok {
		return PathSet{}, false
	}
	return ss[i], true
}

func cmpSet(a, b PathSet) int {
	if c := cmp.Compare(a.Dst, b.Dst); c != 0 {
		return c
	}
	if c := slices.Compare(a.Via, b.Via); c != 0 {
		return c
	}
	return slices.Compare(a.Allow, b.Allow)
}

// EdgeCost is the interactive cost and usability of e:
// the cheaper of its two directions (Link). known is false when no side
// is fresh (the edge is not in the graph).
func EdgeCost(in Inputs, e Edge) (cost uint64, usable, known bool) {
	state, known := in.edgeState(e)
	return state.Cost, state.Usable, known
}

func (in Inputs) edgeState(e Edge) (EdgeState, bool) {
	ab, up, lossDown, known := in.link(e.A, e.B)
	if !up || lossDown {
		return EdgeState{Available: up}, known
	}
	ba, _, _, _ := in.link(e.B, e.A)
	cost := min(pathcost.Edge(ab, pathcost.Interactive), pathcost.Edge(ba, pathcost.Interactive))
	return EdgeState{Cost: cost, Usable: true, Available: true}, true
}

// Link is the edge from→to as the cost model takes it, with its
// usability: the fresher of the two fresh sides decides Up and whether
// loss reaches LossDown. RTT, loss and queueing delay are the larger fresh
// ones; the rate from→to is the smaller known of from's outbound and to's
// inbound report; the fee is paid at both ends — from's uplink for the
// bytes it sends plus to's for the bytes it receives, each end's uplink
// being the dialled one on the accepting end and the default on the
// dialling end; an exhausted pair quota exhausts it; the bias is the
// pair's plus to's. known is false when no side is fresh.
func (in Inputs) Link(from, to NodeID) (l pathcost.Link, usable, known bool) {
	l, up, lossDown, known := in.link(from, to)
	return l, up && !lossDown, known
}

func (in Inputs) link(from, to NodeID) (l pathcost.Link, up, lossDown, known bool) {
	t := in.Topo
	e := MakeEdge(from, to)
	if t.Cover[e].Mode == LinkForbid {
		return l, false, false, true
	}
	a, ok := t.Nodes[from]
	if !ok || !a.Active {
		return l, false, false, true
	}
	b, ok := t.Nodes[to]
	if !ok || !b.Active {
		return l, false, false, true
	}
	sides := in.Links[e]
	fresh := func(m LinkMeas) bool { return m.At != 0 && in.Now-m.At <= 3*t.KeepaliveMS() }
	var side *LinkMeas
	var rates []uint64
	fromUp, toUp := "", "" // the ends' uplinks (default)
	for i := range sides {
		m := &sides[i]
		if !fresh(*m) {
			continue
		}
		if side == nil || m.At > side.At {
			side = m
		}
		l.RTTus = max(l.RTTus, m.RTTus)
		l.LossPPM = max(l.LossPPM, m.LossPPM)
		l.QueueUs = max(l.QueueUs, m.QueueUs)
		l.NoDatagram = l.NoDatagram || m.NoDatagram
		if m.Uplink != "" { // the reporter dialled: the other end's uplink
			if m.From == from {
				toUp = m.Uplink
			} else {
				fromUp = m.Uplink
			}
		}
		r := m.RateInBps // the to end's inbound
		if m.From == from {
			r = m.RateOutBps
		}
		if r > 0 {
			rates = append(rates, r)
		}
	}
	if side == nil {
		return l, false, false, false
	}
	if !side.Up {
		return l, false, false, true
	}
	if len(rates) > 0 {
		l.RateBps = slices.Min(rates)
	}
	l.Fee = a.fee(fromUp, true).Add(b.fee(toUp, false))
	if _, ok := t.Exhausted[e]; ok {
		l.Fee.Exhausted = true // the pair's quota is spent
	}
	l.BiasUs = t.Cover[e].BiasUs + b.BiasUs
	return l, true, side.LossPPM >= pathcost.LossDown, true
}

// graph is the usable edges of one computation by endpoint, and the
// directed link costs per class.
type graph struct {
	in    Inputs
	adj   map[NodeID][]NodeID // ascending neighbours over usable edges
	edges map[Edge]EdgeState
	links map[[2]NodeID]pathcost.Link
	// allow is the route.nodes of the demand being computed (nil: none).
	allow []NodeID
}

func buildGraph(in Inputs) *graph {
	g := &graph{in: in, adj: map[NodeID][]NodeID{}, edges: map[Edge]EdgeState{}, links: map[[2]NodeID]pathcost.Link{}}
	for e := range in.Links {
		g.addEdge(e)
	}
	for e := range in.Topo.Cover {
		g.addEdge(e)
	}
	for n := range g.adj {
		slices.Sort(g.adj[n])
		g.adj[n] = slices.Compact(g.adj[n])
	}
	return g
}

func (g *graph) addEdge(e Edge) {
	if _, ok := g.edges[e]; ok {
		return
	}
	state, _ := g.in.edgeState(e)
	g.edges[e] = state
	if state.Usable {
		g.adj[e.A] = append(g.adj[e.A], e.B)
		g.adj[e.B] = append(g.adj[e.B], e.A)
	}
}

func (g *graph) usable(e Edge) bool { return g.edges[e].Usable }

// link is the directed link from→to (cached).
func (g *graph) link(from, to NodeID) pathcost.Link {
	k := [2]NodeID{from, to}
	if l, ok := g.links[k]; ok {
		return l
	}
	l, _, _, _ := g.in.link(from, to)
	g.links[k] = l
	return l
}

// dcost is the search weight of the usable edge from→to for class c:
// the link's own cost (bias and fees included; no fixed cost replaces
// the measurement).
func (g *graph) dcost(from, to NodeID, c pathcost.Class) uint64 {
	return pathcost.Edge(g.link(from, to), c)
}

// pathLinks are the directed links of the path p from src.
func (g *graph) pathLinks(src NodeID, p []NodeID) []pathcost.Link {
	ls := make([]pathcost.Link, 0, len(p))
	prev := src
	for _, n := range p {
		ls = append(ls, g.link(prev, n))
		prev = n
	}
	return ls
}

// pathCost is the cost of the usable path p from src for class c: the
// cost model over its directed links.
func (g *graph) pathCost(src NodeID, p []NodeID, c pathcost.Class) uint64 {
	return pathcost.Path(g.pathLinks(src, p), c)
}

// pathAllowed checks the constraints shared by selection and recovery.
func (g *graph) pathAllowed(src NodeID, p []NodeID, via []NodeID) bool {
	if len(p) == 0 || len(p) > g.in.Topo.MaxHops || !g.allowed(src) || !g.in.Topo.Nodes[src].Active {
		return false
	}
	prev := src
	hit := len(via) == 0 || slices.Contains(via, src)
	for i, n := range p {
		if !g.allowed(n) || !g.in.Topo.Nodes[n].Active || i < len(p)-1 && !g.transit(n) {
			return false
		}
		if g.in.Topo.Cover[MakeEdge(prev, n)].Mode == LinkForbid {
			return false
		}
		hit = hit || slices.Contains(via, n)
		prev = n
	}
	return hit
}

// pathUp can use explicit availability when comparing recovered paths.
func (g *graph) pathUp(src NodeID, p []NodeID, availabilityOnly bool) bool {
	for _, n := range p {
		edge := g.edges[MakeEdge(src, n)]
		if !edge.Usable && !(availabilityOnly && edge.Available) {
			return false
		}
		src = n
	}
	return true
}

// pathUsable reports p from src usable in g, with its current
// interactive cost.
func (g *graph) pathUsable(src NodeID, p []NodeID, via []NodeID) (uint64, bool) {
	if !g.pathAllowed(src, p, via) || !g.pathUp(src, p, false) {
		return 0, false
	}
	return g.pathCost(src, p, pathcost.Interactive), true
}

func (g *graph) transit(n NodeID) bool {
	x, ok := g.in.Topo.Nodes[n]
	return ok && x.Active && x.Transit && g.allowed(n)
}

// allowed reports n within the current demand's Allow.
func (g *graph) allowed(n NodeID) bool {
	if g.allow == nil {
		return true
	}
	_, ok := slices.BinarySearch(g.allow, n)
	return ok
}

// edgesOf are the edges of p from src.
func edgesOf(src NodeID, p []NodeID) []Edge {
	out := make([]Edge, 0, len(p))
	prev := src
	for _, n := range p {
		out = append(out, MakeEdge(prev, n))
		prev = n
	}
	return out
}
