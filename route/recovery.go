package route

import (
	"fmt"
	"slices"
)

// Allow for staggered peer probes while bounding the age of a round.
const recoveryProbeAgePeriods = 2

// recovery is the evidence for returning to Pending.Hops after an outage.
// Each hop on both paths must have a new observation to complete a round.
type recovery struct {
	rounds  uint64
	samples []int64
}

// recover handles a first path lost to an outage, preserving its identity
// even when it drops out of the candidate set or no fallback is available.
func (g *graph) recover(d Demand, via []NodeID, old *PathSet, p Pending, rebuild bool) (PathSet, Pending, bool) {
	var current []NodeID
	if old != nil {
		current = old.Paths[0].Hops
	}
	allowed := g.pathAllowed(d.Src, current, via)
	usable := allowed && g.pathUp(d.Src, current, true)
	if p.recovery == nil {
		if usable || !allowed {
			return PathSet{}, p, false
		}
		p = Pending{Hops: current, Since: g.in.Now, recovery: &recovery{}}
	} else if !g.pathAllowed(d.Src, p.Hops, via) {
		return PathSet{}, Pending{}, false
	}
	available := g.pathUp(d.Src, p.Hops, true)
	if !available {
		p.Since, p.recovery = g.in.Now, &recovery{}
	}

	if !usable {
		ps := g.candidates(d, via)
		if available {
			g.prefer(&ps, d.Src, p.Hops, "recovered: previous path available while current path is down")
			return ps, Pending{}, true
		}
		ps.PreferFirst = len(ps.Paths) > 0
		p.recovery = &recovery{}
		return ps, p, true
	}

	if available {
		var ready bool
		p, ready = g.recoveryRound(d.Src, current, p)
		if ready {
			ps := g.candidates(d, via)
			g.prefer(&ps, d.Src, p.Hops, fmt.Sprintf("recovered: RTT at least %d%% lower for %d new probe rounds", g.in.Topo.Switch.GainPct, g.in.Topo.Switch.Hold))
			return ps, Pending{}, true
		}
	}
	if !rebuild {
		return *old, p, true
	}
	ps := g.candidates(d, via)
	g.prefer(&ps, d.Src, current, "kept: awaiting recovery of the previous path")
	return g.settle(ps, old), p, true
}

func (g *graph) prefer(ps *PathSet, src NodeID, hops []NodeID, why string) {
	i := slices.IndexFunc(ps.Paths, func(p Path) bool { return slices.Equal(p.Hops, hops) })
	if i >= 0 {
		copy(ps.Paths[1:i+1], ps.Paths[:i])
		copy(ps.Why[1:i+1], ps.Why[:i])
	} else {
		ps.Paths = append(ps.Paths, Path{})
		ps.Why = append(ps.Why, "")
		copy(ps.Paths[1:], ps.Paths)
		copy(ps.Why[1:], ps.Why)
		limit := min(len(ps.Paths), g.in.Topo.Candidates)
		ps.Paths, ps.Why = ps.Paths[:limit], ps.Why[:limit]
	}
	ps.Paths[0], ps.Why[0], ps.PreferFirst = *g.costed(src, hops), why, true
}

func (g *graph) recoveryRound(src NodeID, current []NodeID, p Pending) (Pending, bool) {
	r := *p.recovery
	stamps := make([]int64, 0, len(p.Hops)+len(current))
	cand, stamps := g.probedPath(src, p.Hops, p.Since, stamps)
	cur, stamps := g.probedPath(src, current, p.Since, stamps)
	if cand == 0 || cur == 0 {
		r.rounds = 0
		p.recovery = &r
		return p, false
	}
	for i, at := range r.samples {
		if stamps[i] <= at {
			return p, false
		}
		if stamps[i]-at > recoveryProbeAgePeriods*g.in.Topo.ProbeIntervalMS {
			r.rounds = 0
		}
	}
	r.samples = stamps
	if g.in.Topo.Switch.Better(cand, cur) {
		r.rounds++
	} else {
		r.rounds = 0
	}
	p.recovery = &r
	return p, r.rounds >= g.in.Topo.Switch.Hold
}

// probedPath sums per-hop round trips made with the same measurement
// method. A failed, stale or pre-outage observation invalidates the path.
func (g *graph) probedPath(src NodeID, hops []NodeID, since int64, stamps []int64) (uint64, []int64) {
	var rtt uint64
	for _, n := range hops {
		var latest LinkMeas
		for _, m := range g.in.Links[MakeEdge(src, n)] {
			if m.ProbeAt <= since || m.ProbeAt > g.in.Now || g.in.Now-m.ProbeAt > recoveryProbeAgePeriods*g.in.Topo.ProbeIntervalMS {
				continue
			}
			if m.ProbeAt > latest.ProbeAt {
				latest = m
			}
		}
		if !latest.Up || latest.ProbeRTTus == 0 {
			return 0, stamps
		}
		rtt += uint64(latest.ProbeRTTus)
		stamps = append(stamps, latest.ProbeAt)
		src = n
	}
	return rtt, stamps
}
