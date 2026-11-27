package route

import (
	"slices"
	"testing"

	"github.com/djylb/pathcost"
)

func TestRecoveryRTT(t *testing.T) {
	for _, tc := range []struct {
		name string
		rtt  uint32
		back bool
	}{
		{"inclusive 20 percent", 240_000, true},
		{"below required gain", 241_000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.run(true, []NodeID{3})
			f.fail(f.now+1, 1, 3)
			f.run(false, []NodeID{2, 3})

			// The original path is up, with a restated pre-failure RTT
			// and enough remembered loss to exclude it from cost search.
			f.now += f.topo.ProbeIntervalMS
			f.put(1, 3, true, f.now-f.topo.ProbeIntervalMS, tc.rtt, pathcost.LossDown)
			f.put(1, 2, true, f.now, 150_000, 0)
			f.put(2, 3, true, f.now, 150_000, 0)
			f.run(false, []NodeID{2, 3})
			for i := range 3 {
				f.round(tc.rtt)
				if !tc.back {
					// A large cost advantage cannot bypass RTT hysteresis.
					edge := MakeEdge(1, 3)
					sides := f.links[edge]
					sides[0].RTTus, sides[0].LossPPM = 1_000, 0
					f.links[edge] = sides
				}
				want := []NodeID{2, 3}
				if i == 2 && tc.back {
					want = []NodeID{3}
				}
				ps := f.run(false, want)
				if !ps.PreferFirst {
					t.Fatal("recovery order must apply to all classes")
				}
				// Repeated computations and availability keepalives
				// over the same RTT observations cannot add a round.
				for range 3 {
					f.refresh()
					f.run(false, want)
				}
			}
		})
	}
}

func TestRecoveryRestartsAfterInvalidRound(t *testing.T) {
	for _, interruption := range []string{"insufficient gain", "miss", "stale", "outage"} {
		t.Run(interruption, func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.run(true, []NodeID{3})
			f.fail(f.now+1, 1, 3)
			f.run(false, []NodeID{2, 3})
			for range 2 {
				f.round(240_000)
				f.run(false, []NodeID{2, 3})
			}
			switch interruption {
			case "insufficient gain":
				f.round(241_000)
			case "miss":
				f.round(0)
			case "stale":
				f.now += 3 * f.topo.ProbeIntervalMS
				f.refresh()
			case "outage":
				f.fail(f.now+1, 1, 3)
			}
			f.run(false, []NodeID{2, 3})
			for range 2 {
				f.round(240_000)
				f.run(false, []NodeID{2, 3})
			}
			f.round(240_000)
			f.run(false, []NodeID{3})
		})
	}
}

func TestRecoveryRequiresAllHopsAndRespectsAvailability(t *testing.T) {
	f := newRecoveryFixture(t)
	f.run(true, []NodeID{3})
	f.fail(f.now+1, 1, 3)
	f.run(false, []NodeID{2, 3})
	f.round(240_000)
	f.run(false, []NodeID{2, 3})

	// A fresh original path and one fresh relay hop cannot make a round.
	for range 3 {
		f.now += f.topo.ProbeIntervalMS / 2
		f.put(1, 3, true, f.now, 240_000, pathcost.LossDown)
		f.put(1, 2, true, f.now, 150_000, 0)
		f.run(false, []NodeID{2, 3})
	}

	// The fallback is lost. The available original path wins immediately.
	f.fail(f.now+1, 2, 3)
	f.run(false, []NodeID{3})
}

func TestRecoveryDropsForbiddenPath(t *testing.T) {
	f := newRecoveryFixture(t)
	f.run(true, []NodeID{3})
	f.fail(f.now+1, 1, 3)
	f.run(false, []NodeID{2, 3})
	f.topo.Cover[MakeEdge(1, 3)] = Cover{Mode: LinkForbid}
	f.round(240_000)
	ps := f.run(true, []NodeID{2, 3})
	if len(f.table.pending) != 0 || len(ps.Paths) != 1 {
		t.Fatalf("forbidden path retained: %v, pending=%v", hops(ps), f.table.pending)
	}
}

type recoveryFixture struct {
	t     *testing.T
	topo  *Topo
	links LinkTable
	table *PathTable
	now   int64
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	f := &recoveryFixture{t: t, topo: topo(3, Demand{Src: 1, Dst: 3}), links: LinkTable{}, now: now}
	f.put(1, 3, true, now, 240_000, 0)
	f.put(1, 2, true, now, 150_000, 0)
	f.put(2, 3, true, now, 150_000, 0)
	return f
}

func (f *recoveryFixture) put(a, b NodeID, up bool, stamp int64, rtt, loss uint32) {
	e := MakeEdge(a, b)
	m := f.links[e]
	m[e.Side(a)] = LinkMeas{Up: up, At: f.now, RTTus: rtt, ProbeAt: stamp, ProbeRTTus: rtt, LossPPM: loss}
	f.links[e] = m
}

func (f *recoveryFixture) fail(at int64, a, b NodeID) {
	f.now = at
	f.put(a, b, false, f.now, 0, 0)
}

func (f *recoveryFixture) round(rtt uint32) {
	f.now += f.topo.ProbeIntervalMS
	f.put(1, 3, true, f.now, rtt, pathcost.LossDown)
	f.put(1, 2, true, f.now, 150_000, 0)
	f.put(2, 3, true, f.now, 150_000, 0)
}

func (f *recoveryFixture) refresh() {
	f.now++
	for edge, sides := range f.links {
		sides[0].At = f.now
		f.links[edge] = sides
	}
}

func (f *recoveryFixture) run(full bool, want []NodeID) PathSet {
	f.t.Helper()
	f.table, _ = Compute(Inputs{Topo: f.topo, Links: f.links, Now: f.now}, f.table, full)
	ps, ok := f.table.Set(1, 3, nil)
	if !ok || !slices.Equal(ps.Paths[0].Hops, want) {
		f.t.Fatalf("at %d: candidates %v, want first %v", f.now, hops(ps), want)
	}
	return ps
}

func TestRecoveryWaitsForReport(t *testing.T) {
	f := newRecoveryFixture(t)
	f.topo.Switch.Hold = 1
	f.run(true, []NodeID{3})
	f.fail(f.now+1, 1, 3)
	f.run(false, []NodeID{2, 3})
	f.round(1000)
	f.put(1, 3, true, now, 1000, 0)
	f.put(3, 1, true, f.now, 1000, 0)
	e := MakeEdge(1, 3)
	sides := f.links[e]
	sides[1].At++
	f.links[e] = sides
	f.run(false, []NodeID{2, 3})
	f.now++
	f.run(false, []NodeID{3})
}
