package route

import (
	"cmp"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/djylb/pathcost"
)

// NodeID is a node id. Ids order the tie-breaks, so equal inputs give
// equal outputs.
type NodeID int64

// Edge is an unordered node pair with A <= B.
type Edge struct{ A, B NodeID }

// MakeEdge is the edge of a and b in either order.
func MakeEdge(a, b NodeID) Edge {
	if a > b {
		a, b = b, a
	}
	return Edge{a, b}
}

// Other returns the opposite endpoint. It panics if n is outside e.
func (e Edge) Other(n NodeID) NodeID {
	if e.Side(n) == 0 {
		return e.B
	}
	return e.A
}

// Side returns the index of n's report in [LinkTable]: 0 for A, 1 for B.
// It panics if n is outside e.
func (e Edge) Side(n NodeID) int {
	switch n {
	case e.A:
		return 0
	case e.B:
		return 1
	default:
		panic("route: node is not an endpoint of the edge")
	}
}

func (e Edge) String() string {
	return strconv.FormatInt(int64(e.A), 10) + "-" + strconv.FormatInt(int64(e.B), 10)
}

// LinkMeas is one end's report of a link.
type LinkMeas struct {
	Up      bool
	RTTus   uint32 // smoothed by the reporter, queueing taken out
	LossPPM uint32
	At      int64 // the receive time, monotonic ms; 0: none

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
	// LinkAuto includes the edge when measurements show it is usable.
	LinkAuto LinkMode = iota
	// LinkMust marks the pair for probing even when no demand uses it.
	LinkMust
	// LinkForbid excludes the pair from routing and probing.
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

// key identifies a demand in the table's pending switches.
func (d Demand) key() string {
	var b strings.Builder
	b.WriteString(strconv.FormatInt(int64(d.Src), 10))
	b.WriteByte(':')
	b.WriteString(strconv.FormatInt(int64(d.Dst), 10))
	if len(d.Via) > 0 {
		appendNodeKey(&b, 'v', d.Via)
	}
	if d.Allow != nil {
		appendNodeKey(&b, 'a', d.Allow)
	}
	return b.String()
}

func appendNodeKey(b *strings.Builder, prefix byte, nodes []NodeID) {
	b.WriteByte(prefix)
	for i, n := range nodes {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(strconv.FormatInt(int64(n), 10))
	}
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
	// measurement periods). The table publishes effective links exactly,
	// including changes below Switch.PushPct.
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

// Norm returns a shallow copy with default settings filled in and Switch
// normalized. The maps and slices are shared with t.
func (t Topo) Norm() Topo {
	if t.MaxHops <= 0 {
		t.MaxHops = DefaultMaxHops
	}
	if t.Candidates <= 0 {
		t.Candidates = DefaultCandidates
	}
	if t.RelayProbe <= 0 {
		t.RelayProbe = DefaultRelayProbe
	}
	t.ProbeIntervalMS = boundedPeriods(t.ProbeIntervalMS, 1)
	t.Switch = t.Switch.Norm()
	return t
}

func (in Inputs) fresh(m LinkMeas) bool {
	return m.At > 0 && m.At <= in.Now && in.Now-m.At <= boundedPeriods(in.Topo.ProbeIntervalMS, 15)
}

// KeepaliveMS is the longest a report stays without a refresh (5 ×
// the probe interval); a report older than 3 × it is not fresh.
func (t *Topo) KeepaliveMS() int64 { return boundedPeriods(t.ProbeIntervalMS, 5) }

func boundedPeriods(period, n int64) int64 {
	if period <= 0 {
		period = DefaultProbeIntervalMS
	}
	if period > math.MaxInt64/n {
		return math.MaxInt64
	}
	return period * n
}

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
		if !ok || n.ID != m.ID || n.Load != m.Load || n.Active != m.Active || n.Transit != m.Transit || n.BiasUs != m.BiasUs || !maps.Equal(n.Fees, m.Fees) {
			return false
		}
	}
	for e, c := range t.Cover {
		if other, ok := o.Cover[e]; !ok || other != c {
			return false
		}
	}
	for i, d := range t.Demand {
		x := o.Demand[i]
		if d.Src != x.Src || cmpSet(d.set(), x.set()) != 0 {
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
		return cmpSet(a.set(), b.set())
	})
	return slices.CompactFunc(ds, func(a, b Demand) bool {
		return a.Src == b.Src && cmpSet(a.set(), b.set()) == 0
	})
}

// Inputs are one computation's inputs.
type Inputs struct {
	Topo  *Topo
	Links LinkTable
	Now   int64 // nonnegative monotonic milliseconds; negative values use zero
	// Changed optionally marks edges for reconsideration. Compute detects effective link changes itself.
	Changed map[Edge]struct{}
}

// Path is one candidate: the hops after the source, the last the
// destination (direct = [dst]), and its directed links for exact repricing.
type Path struct {
	Hops       []NodeID
	CostUs     uint64
	BulkCostUs uint64
	RtCostUs   uint64
	Links      []pathcost.Link // exact ordered segments for concatenation
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
	// Why gives one human-readable diagnostic per path in the same order.
	// The text is unstable and must not be parsed.
	Why []string
}

// PathTable is a read-only snapshot of path sets by source, the peers
// each source probes, and the state carried into the next computation.
type PathTable struct {
	BySrc map[NodeID][]PathSet
	Probe map[NodeID][]NodeID
	// Gen grows when Compute marks at least one source as changed.
	Gen uint64
	// Recheck is the earliest time (ms) a pending switch is due; 0: none.
	// The caller recomputes then.
	Recheck int64

	// badSince is when an edge of the demand closure was last seen
	// usable→not (or first wanted); the relay probes start one probe
	// interval later.
	badSince map[Edge]int64
	// used records the effective edge states in this snapshot.
	used map[Edge]EdgeState
	// pending holds switches keyed by demand: a cheaper path or a failed path.
	pending map[string]pendingSwitch
}

// pendingSwitch is a first path waiting to replace the current one. since is
// when it became cheaper, or when the failed path was last seen down.
type pendingSwitch struct {
	hops     []NodeID
	since    int64
	recovery *recovery
}

// EdgeState is an edge as one computation saw it.
type EdgeState struct {
	Directions [2]pathcost.Link // from A, from B
	Cost       uint64
	Usable     bool
	Available  bool // the latest fresh report says Up
}

// Edge returns a copy of e's state. The bool reports whether the edge
// was included in the snapshot, even if stale or unusable. A nil table
// returns the zero state and false.
func (t *PathTable) Edge(e Edge) (EdgeState, bool) {
	if t == nil {
		return EdgeState{}, false
	}
	state, ok := t.used[e]
	return state, ok
}

// Set returns src's set for (dst, via) without an Allow limit.
func (t *PathTable) Set(src, dst NodeID, via []NodeID) (PathSet, bool) {
	return t.SetOf(Demand{Src: src, Dst: dst, Via: via})
}

// SetOf returns a borrowed read-only set. Its slices can span table generations.
// Compute copies demand keys into the snapshot. Clone the returned set to edit its slices.
func (t *PathTable) SetOf(d Demand) (PathSet, bool) {
	if t == nil {
		return PathSet{}, false
	}
	ss := t.BySrc[d.Src]
	i, ok := slices.BinarySearchFunc(ss, d.set(), cmpSet)
	if !ok {
		return PathSet{}, false
	}
	return ss[i], true
}

func (d Demand) set() PathSet { return PathSet{Dst: d.Dst, Via: d.Via, Allow: d.Allow} }
func cmpSet(a, b PathSet) int {
	if c := cmp.Compare(a.Dst, b.Dst); c != 0 {
		return c
	}
	if c := slices.Compare(a.Via, b.Via); c != 0 {
		return c
	}
	if a.Allow == nil && b.Allow != nil {
		return -1
	}
	if b.Allow == nil && a.Allow != nil {
		return 1
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
	if !up {
		return EdgeState{}, known
	}
	ba, _, _, _ := in.link(e.B, e.A)
	cost := min(pathcost.Edge(ab, pathcost.Interactive), pathcost.Edge(ba, pathcost.Interactive))
	return EdgeState{Directions: [2]pathcost.Link{ab, ba}, Cost: cost, Usable: !lossDown, Available: true}, true
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
	if from == to || t.Cover[e].Mode == LinkForbid {
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
	fromSide := e.Side(from)

	var side *LinkMeas
	var rates []uint64
	fromUp, toUp := "", "" // the ends' uplinks (default)
	for i := range sides {
		m := &sides[i]
		if !in.fresh(*m) {
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
			if i == fromSide {
				toUp = m.Uplink
			} else {
				fromUp = m.Uplink
			}
		}
		r := m.RateInBps // the to end's inbound
		if i == fromSide {
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
	l.BiasUs = pathcost.AddBias(t.Cover[e].BiasUs, b.BiasUs)
	return l, true, side.LossPPM >= pathcost.LossDown, true
}

// graph is the usable edges of one computation by endpoint, and the
// directed link costs per class.
type graph struct {
	in    Inputs
	adj   map[NodeID][]NodeID // ascending neighbours over usable edges
	edges map[Edge]EdgeState

	// allow is the route.nodes of the demand being computed (nil: none).
	allow    []NodeID
	excluded []NodeID // original first path awaiting recovery evidence
}

func buildGraph(in Inputs) *graph {
	g := &graph{in: in, adj: map[NodeID][]NodeID{}, edges: map[Edge]EdgeState{}}
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

// pathLinks are the directed links of the path p from src.
func (g *graph) pathLinks(src NodeID, p []NodeID) []pathcost.Link {
	ls := make([]pathcost.Link, 0, len(p))
	prev := src
	for _, n := range p {
		e := MakeEdge(prev, n)
		ls = append(ls, g.edges[e].Directions[e.Side(prev)])
		prev = n
	}
	return ls
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
		up := edge.Usable || availabilityOnly && edge.Available
		if !up {
			return false
		}
		src = n
	}
	return true
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

// Clone returns an independently editable copy of a published set.
func (s PathSet) Clone() PathSet {
	s.Via = slices.Clone(s.Via)
	s.Allow = slices.Clone(s.Allow)
	s.Why = slices.Clone(s.Why)
	s.Paths = slices.Clone(s.Paths)
	for i, p := range s.Paths {
		s.Paths[i].Hops = slices.Clone(p.Hops)
		s.Paths[i].Links = slices.Clone(p.Links)
	}
	return s
}
