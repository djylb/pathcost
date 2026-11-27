package route

import (
	"fmt"
	"slices"

	"github.com/djylb/pathcost"
)

// Allow for staggered peer probes while bounding the age of a round.
const recoveryProbeAgePeriods = 2

// recovery is the evidence for returning to the path that failed.
// Each hop on both paths must have a new observation to complete a round.
type recovery struct {
	alternative pendingSwitch
	rounds      uint64
	samples     []int64
}

// recover handles a first path lost to an outage, preserving its identity
// even when it drops out of the candidate set or no fallback is available.
func (g *graph) recover(d Demand, via []NodeID, old PathSet, p pendingSwitch, rebuild bool) (PathSet, pendingSwitch, bool) {
	var current []NodeID
	if len(old.Paths) > 0 {
		current = old.Paths[0].Hops
	}
	allowed := g.pathAllowed(d.Src, current, via)
	usable := allowed && g.pathUp(d.Src, current, true)
	if p.recovery == nil {
		if usable || !allowed {
			return PathSet{}, p, false
		}
		p = pendingSwitch{hops: current, since: g.in.Now, recovery: &recovery{}}
	} else if !g.pathAllowed(d.Src, p.hops, via) {
		return PathSet{}, pendingSwitch{}, false
	}
	available := g.pathUp(d.Src, p.hops, true)
	if !available {
		p.since, p.recovery = g.in.Now, &recovery{alternative: p.recovery.alternative}
	}

	if !usable {
		if available {
			ps := g.recoveredSet(d, via, p.hops, "recovered: previous path available while current path is down")
			return ps, pendingSwitch{}, true
		}
		ps := g.candidates(d, via, nil, g.winners(d, via))
		ps.PreferFirst = len(ps.Paths) > 0
		p.recovery = &recovery{}
		return ps, p, true
	}

	if available {
		var ready bool
		p, ready = g.recoveryRound(d.Src, current, p)
		if ready {
			ps := g.recoveredSet(d, via, p.hops, fmt.Sprintf("recovered: RTT at least %d%% lower for %d new probe rounds", g.in.Topo.Switch.GainPct, g.in.Topo.Switch.Hold))
			return ps, pendingSwitch{}, true
		}
	}
	if !rebuild {
		// Probe evidence can change without changing any effective link.
		return old, p, true
	}
	g.excluded = p.hops
	ps, alt := g.pathSet(d, via, old, p.recovery.alternative)
	g.excluded = nil
	r := *p.recovery
	if len(ps.Paths) == 0 || !slices.Equal(current, ps.Paths[0].Hops) {
		r.samples = nil
		r.rounds = 0
	}
	r.alternative = alt
	p.recovery = &r
	return ps, p, true
}

func (g *graph) recoveredSet(d Demand, via, hops []NodeID, why string) PathSet {
	winners := g.winners(d, via)
	ps := g.candidates(d, via, g.retained(d.Src, hops, winners), winners)
	ps.Why[0], ps.PreferFirst = why, true
	return ps
}

func (g *graph) recoveryRound(src NodeID, current []NodeID, p pendingSwitch) (pendingSwitch, bool) {
	r := *p.recovery
	stamps := make([]int64, 0, len(p.hops)+len(current))
	cand, stamps := g.probedPath(src, p.hops, p.since, stamps)
	cur, stamps := g.probedPath(src, current, p.since, stamps)
	if cand == 0 || cur == 0 {
		r.rounds = 0
		p.recovery = &r
		return p, false
	}
	for i, at := range r.samples {
		if stamps[i] <= at {
			return p, false
		}
		if stamps[i]-at > boundedPeriods(g.in.Topo.ProbeIntervalMS, recoveryProbeAgePeriods) {
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
	links := make([]pathcost.Link, 0, len(hops))
	for _, n := range hops {
		var latest LinkMeas
		for _, m := range g.in.Links[MakeEdge(src, n)] {
			if !g.in.fresh(m) || m.ProbeAt <= since || m.ProbeAt > g.in.Now || g.in.Now-m.ProbeAt > boundedPeriods(g.in.Topo.ProbeIntervalMS, recoveryProbeAgePeriods) {
				continue
			}
			if m.ProbeAt > latest.ProbeAt {
				latest = m
			}
		}
		if !latest.Up || latest.ProbeRTTus == 0 {
			return 0, stamps
		}
		links = append(links, pathcost.Link{RTTus: latest.ProbeRTTus})
		stamps = append(stamps, latest.ProbeAt)
		src = n
	}
	return pathcost.Path(links, pathcost.Interactive), stamps
}
