package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/djylb/pathcost"
	"github.com/djylb/pathcost/probe"
	"github.com/djylb/pathcost/route"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "standalone:", err)
		os.Exit(1)
	}
}

// Scenario is the input file.
type Scenario struct {
	// Lambda weighs unit prices (0: pathcost.LambdaBalanced).
	Lambda          uint64       `json:"lambda,omitempty"`
	MaxHops         int          `json:"max_hops,omitempty"`
	Candidates      int          `json:"candidates,omitempty"`
	ProbeIntervalMS int64        `json:"probe_interval_ms,omitempty"`
	Switch          SwitchJSON   `json:"switch,omitzero"`
	Nodes           []NodeJSON   `json:"nodes"`
	Pairs           []PairJSON   `json:"pairs,omitempty"`
	Links           []LinkJSON   `json:"links"`
	Demand          []DemandJSON `json:"demand,omitempty"` // none: every ordered pair
	Steps           []StepJSON   `json:"steps,omitempty"`
}

// SwitchJSON configures the gain and hold for a first-path switch.
// Zero values select the defaults in pathcost.Hysteresis.
type SwitchJSON struct {
	GainPct uint64 `json:"gain_pct,omitempty"`
	Hold    uint64 `json:"hold,omitempty"`
}

// NodeJSON is one node; every node is active.
type NodeJSON struct {
	ID         int64  `json:"id"`
	NoTransit  bool   `json:"no_transit,omitempty"`
	BiasUs     int64  `json:"bias_us,omitempty"`
	PriceIn    uint64 `json:"price_in,omitempty"`  // unit price per GiB received
	PriceOut   uint64 `json:"price_out,omitempty"` // unit price per GiB sent
	QuotaUsed  uint64 `json:"quota_used,omitempty"`
	Quota      uint64 `json:"quota,omitempty"`       // bytes per period; 0 none
	ElapsedPPM uint32 `json:"elapsed_ppm,omitempty"` // share of the period gone
}

// PairJSON is a node pair override.
type PairJSON struct {
	A         int64  `json:"a"`
	B         int64  `json:"b"`
	Mode      string `json:"mode,omitempty"` // auto, must, forbid
	BiasUs    int64  `json:"bias_us,omitempty"`
	Exhausted bool   `json:"exhausted,omitempty"`
}

// LinkJSON is a measurement of a→b (both ends report symmetric rates).
type LinkJSON struct {
	A          int64   `json:"a"`
	B          int64   `json:"b"`
	Down       bool    `json:"down,omitempty"`
	RTTms      float64 `json:"rtt_ms"`
	LossPct    float64 `json:"loss_pct,omitempty"`
	RateMbps   float64 `json:"rate_mbps,omitempty"`
	QueueMs    float64 `json:"queue_ms,omitempty"`
	NoDatagram bool    `json:"no_datagram,omitempty"`
	Remove     bool    `json:"remove,omitempty"`
}

// DemandJSON is one demand.
type DemandJSON struct {
	Src   int64   `json:"src"`
	Dst   int64   `json:"dst"`
	Via   []int64 `json:"via,omitempty"`
	Allow []int64 `json:"allow,omitempty"`
}

// StepJSON changes measurements at a time (ms from the start).
type StepJSON struct {
	AtMS  int64      `json:"at_ms"`
	Links []LinkJSON `json:"links"`
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("standalone", flag.ContinueOnError)
	fs.SetOutput(stdout)
	asJSON := fs.Bool("json", false, "print JSON lines instead of text")
	replay := fs.Bool("replay", false, "replay a probe trace instead of a scenario")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: standalone [-json] scenario.json | -replay trace.txt (\"-\": stdin)")
	}
	in := stdin
	if p := fs.Arg(0); p != "-" {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		in = f
	}
	if *replay {
		return replayTrace(in, stdout)
	}
	var sc Scenario
	reader := &scenarioReader{src: in}
	dec := json.NewDecoder(reader)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sc); err != nil {
		return fmt.Errorf("scenario: %w", err)
	}
	tail := bufio.NewReader(io.MultiReader(dec.Buffered(), reader))
	for {
		b, err := tail.ReadByte()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("scenario: %w", err)
		}
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			return errors.New("scenario: trailing data")
		}
	}
	return simulate(&sc, stdout, *asJSON)
}

// A decoder may finish the object using bytes returned with a read error.
// Keep the error for the subsequent check for end of input.
type scenarioReader struct {
	src io.Reader
	err error
}

func (r *scenarioReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	n, err := r.src.Read(p)
	r.err = err
	return n, err
}

// Topo is the route topology of sc.
func (sc *Scenario) Topo() (*route.Topo, error) {
	t := &route.Topo{Nodes: map[route.NodeID]route.Node{}, Cover: map[route.Edge]route.Cover{},
		MaxHops: sc.MaxHops, Candidates: sc.Candidates, ProbeIntervalMS: sc.ProbeIntervalMS,
		Switch: pathcost.Hysteresis{GainPct: sc.Switch.GainPct, Hold: sc.Switch.Hold}}
	for _, n := range sc.Nodes {
		if _, dup := t.Nodes[n.ID]; dup {
			return nil, fmt.Errorf("node %d twice", n.ID)
		}
		x := route.Node{ID: n.ID, Active: true, Transit: !n.NoTransit, BiasUs: n.BiasUs}
		scar, ex := pathcost.Progress(n.QuotaUsed, n.Quota, n.ElapsedPPM)
		if n.PriceIn != 0 || n.PriceOut != 0 || scar != 0 || ex {
			x.Fees = map[string]route.UplinkFee{"": {In: pathcost.FeeOf(sc.Lambda, n.PriceIn, scar, ex), Out: pathcost.FeeOf(sc.Lambda, n.PriceOut, scar, ex)}}
		}
		t.Nodes[n.ID] = x
	}
	seen := map[route.Edge]bool{}
	for _, p := range sc.Pairs {
		e := route.MakeEdge(p.A, p.B)
		if seen[e] || p.A == p.B || !t.Nodes[p.A].Active || !t.Nodes[p.B].Active {
			return nil, fmt.Errorf("invalid or duplicate pair %v", e)
		}
		seen[e] = true
		var m route.LinkMode
		switch p.Mode {
		case "", "auto":
		case "must":
			m = route.LinkMust
		case "forbid":
			m = route.LinkForbid
		default:
			return nil, fmt.Errorf("pair %v: mode %q", e, p.Mode)
		}
		if m != route.LinkAuto || p.BiasUs != 0 {
			t.Cover[e] = route.Cover{Mode: m, BiasUs: p.BiasUs}
		}
		if p.Exhausted {
			if t.Exhausted == nil {
				t.Exhausted = map[route.Edge]struct{}{}
			}
			t.Exhausted[e] = struct{}{}
		}
	}
	var ds []route.Demand
	for _, d := range sc.Demand {
		for _, group := range [][]int64{{d.Src, d.Dst}, d.Allow, d.Via} {
			for _, id := range group {
				if !t.Nodes[id].Active {
					return nil, fmt.Errorf("demand %d->%d: unknown node %d", d.Src, d.Dst, id)
				}
			}
		}
		ds = append(ds, route.Demand{Src: d.Src, Dst: d.Dst, Via: sorted(d.Via), Allow: sorted(d.Allow)})
	}
	if len(sc.Demand) == 0 {
		for _, a := range sc.Nodes {
			for _, b := range sc.Nodes {
				if a.ID != b.ID {
					ds = append(ds, route.Demand{Src: a.ID, Dst: b.ID})
				}
			}
		}
	}
	validate := func(ls []LinkJSON) error {
		for i, l := range ls {
			if l.A == l.B || !t.Nodes[l.A].Active || !t.Nodes[l.B].Active {
				return fmt.Errorf("links[%d]: invalid endpoints %d-%d", i, l.A, l.B)
			}
			if !(l.RTTms >= 0 && l.RTTms*1000 <= math.MaxUint32 && l.QueueMs >= 0 && l.QueueMs*1000 <= math.MaxUint32 && l.RateMbps >= 0 && l.RateMbps*125000 < math.Exp2(64) && l.LossPct >= 0 && l.LossPct <= 100) {
				return fmt.Errorf("links[%d]: measurement out of range", i)
			}
		}
		return nil
	}
	if err := validate(sc.Links); err != nil {
		return nil, err
	}
	for i, s := range sc.Steps {
		if s.AtMS < 0 || s.AtMS == math.MaxInt64 {
			return nil, fmt.Errorf("steps[%d].at_ms out of range", i)
		}
		if err := validate(s.Links); err != nil {
			return nil, fmt.Errorf("steps[%d]: %w", i, err)
		}
	}
	t.Demand = route.SortDemand(ds)
	t.Norm()
	return t, nil
}

func sorted(s []int64) []int64 {
	if s == nil {
		return nil
	}
	s = slices.Clone(s)
	slices.Sort(s)
	return slices.Compact(s)
}

// apply puts the measurements ls into lt at now (ms), both ends
// reporting, and returns the edges touched.
func apply(lt route.LinkTable, ls []LinkJSON, now int64) []route.Edge {
	var out []route.Edge
	for _, l := range ls {
		e := route.MakeEdge(l.A, l.B)
		out = append(out, e)
		if l.Remove {
			delete(lt, e)
			continue
		}
		m := route.LinkMeas{Up: !l.Down, RTTus: uint32(l.RTTms * 1000), LossPPM: uint32(l.LossPct * 10_000),
			QueueUs: uint32(l.QueueMs * 1000), NoDatagram: l.NoDatagram, At: now}
		m.ProbeAt = now
		if m.Up && m.RTTus != 0 {
			m.ProbeRTTus = uint32(min(uint64(m.RTTus)+uint64(m.QueueUs), 1<<32-1))
		}
		rate := uint64(l.RateMbps * 125000)
		var s [2]route.LinkMeas
		for i := range s {
			x := m

			x.RateOutBps, x.RateInBps = rate, rate
			s[i] = x
		}
		lt[e] = s
	}
	return out
}

// event is one line of the output.
type event struct {
	Allow       []int64        `json:"allow"`
	PreferFirst bool           `json:"prefer_first"`
	AtMS        int64          `json:"at_ms"`
	Kind        string         `json:"kind"` // paths, switch
	Src         int64          `json:"src"`
	Dst         int64          `json:"dst"`
	Via         []int64        `json:"via,omitempty"`
	Paths       []pathJSON     `json:"paths,omitempty"`
	From        []route.NodeID `json:"from,omitempty"`
	To          []route.NodeID `json:"to,omitempty"`
}

type pathJSON struct {
	Hops   []route.NodeID `json:"hops"`
	CostUs uint64         `json:"cost_us"`
	BulkUs uint64         `json:"bulk_us"`
	RtUs   uint64         `json:"realtime_us"`
	Why    string         `json:"why"`
}

func visiblePaths(ps route.PathSet) []pathJSON {
	var paths []pathJSON
	for i, p := range ps.Paths {
		why := ""
		if i < len(ps.Why) {
			why = ps.Why[i]
		}
		paths = append(paths, pathJSON{Hops: p.Hops, CostUs: p.CostUs, BulkUs: p.BulkCostUs, RtUs: p.RtCostUs, Why: why})
	}
	return paths
}

// simulate runs the scenario: the initial computation, then each step and
// every pending hysteresis deadline. Measurements at a deadline apply first.
func simulate(sc *Scenario, w io.Writer, asJSON bool) (err error) {
	tp, err := sc.Topo()
	if err != nil {
		return err
	}
	slices.SortStableFunc(sc.Steps, func(a, b StepJSON) int { return cmp.Compare(a.AtMS, b.AtMS) })
	const start = 1 // ms; 0 is "no report"
	lt := route.LinkTable{}
	apply(lt, sc.Links, start)
	keep := tp.KeepaliveMS()
	var prev *route.PathTable
	out := bufio.NewWriter(w)
	defer flush(out, &err)

	emit := func(e event) error {
		if asJSON {
			b, err := json.Marshal(e)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "%s\n", b)
			return err
		}
		return printText(out, e)
	}
	step := func(now int64, changed map[route.Edge]struct{}, all bool) error {
		// refresh every report: the scenario's measurements hold until changed
		for e, s := range lt {
			for i := range s {
				if s[i].At != 0 && now-s[i].At > keep/2 {
					s[i].At = now
				}
			}
			lt[e] = s
		}
		pt, _ := route.Compute(route.Inputs{Topo: tp, Links: lt, Now: now, Changed: changed}, prev, prev == nil)
		for _, d := range tp.Demand {
			ps, _ := pt.SetOf(d)
			old, _ := prev.SetOf(d)
			if prev != nil && first(old) != nil && first(ps) != nil && !slices.Equal(first(old), first(ps)) {
				if err := emit(event{AtMS: now - start, Kind: "switch", Src: d.Src, Dst: d.Dst, Via: d.Via, Allow: d.Allow, PreferFirst: ps.PreferFirst, From: first(old), To: first(ps)}); err != nil {
					return err
				}
			}
			paths := visiblePaths(ps)
			if all || prev == nil || old.PreferFirst != ps.PreferFirst || !reflect.DeepEqual(visiblePaths(old), paths) {
				e := event{AtMS: now - start, Kind: "paths", Src: d.Src, Dst: d.Dst, Via: d.Via, Allow: d.Allow, PreferFirst: ps.PreferFirst, Paths: paths}
				if err := emit(e); err != nil {
					return err
				}
			}
		}
		prev = pt
		return nil
	}
	i := 0
	for i < len(sc.Steps) && sc.Steps[i].AtMS == 0 {
		apply(lt, sc.Steps[i].Links, start)
		i++
	}
	if err := step(start, nil, true); err != nil {
		return err
	}
	recheck := func(until int64) error {
		for prev.Recheck != 0 && prev.Recheck <= until {
			due := prev.Recheck
			if err := step(due, map[route.Edge]struct{}{}, false); err != nil {
				return err
			}
			if prev.Recheck != 0 && prev.Recheck <= due {
				return errors.New("recheck did not advance")
			}
		}
		return nil
	}
	for i < len(sc.Steps) {
		s := sc.Steps[i]
		at := start + s.AtMS
		if err := recheck(at - 1); err != nil {
			return err
		}
		changed := map[route.Edge]struct{}{}
		for i < len(sc.Steps) && sc.Steps[i].AtMS == s.AtMS {
			for _, e := range apply(lt, sc.Steps[i].Links, at) {
				changed[e] = struct{}{}
			}
			i++
		}
		if err := step(at, changed, false); err != nil {
			return err
		}
	}
	if err := recheck(math.MaxInt64); err != nil {
		return err
	}
	return nil
}

func first(ps route.PathSet) []route.NodeID {
	if len(ps.Paths) == 0 {
		return nil
	}
	return ps.Paths[0].Hops
}

func printText(w io.Writer, e event) error {
	via := ""
	if len(e.Via) > 0 {
		via = fmt.Sprintf(" via %v", e.Via)
	}
	if e.Allow != nil {
		via += fmt.Sprintf(" allow %v", e.Allow)
	}
	if e.PreferFirst {
		via += " prefer first"
	}
	if e.Kind == "switch" {
		_, err := fmt.Fprintf(w, "%8d ms  switch %d→%d%s: %v → %v\n", e.AtMS, e.Src, e.Dst, via, e.From, e.To)
		return err
	}
	if _, err := fmt.Fprintf(w, "%8d ms  %d→%d%s:\n", e.AtMS, e.Src, e.Dst, via); err != nil {
		return err
	}
	if len(e.Paths) == 0 {
		_, err := fmt.Fprintln(w, "             no path")
		return err
	}
	for _, p := range e.Paths {
		if _, err := fmt.Fprintf(w, "             %v  interactive %d us  bulk %d us  realtime %d us  (%s)\n", p.Hops, p.CostUs, p.BulkUs, p.RtUs, p.Why); err != nil {
			return err
		}
	}
	return nil
}

// replayTrace feeds probe outcomes (lines "<ms> <rtt_ms>" or "<ms> lost";
// "#" comments) through a probe.Estimator and prints the estimate after
// each, with the interactive cost of the link alone.
func replayTrace(in io.Reader, w io.Writer) (err error) {
	var e probe.Estimator
	sc := bufio.NewScanner(in)
	out := bufio.NewWriter(w)
	defer flush(out, &err)

	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 {
			return fmt.Errorf("trace line %d: want <ms> <rtt_ms>|lost", n)
		}
		at, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			return fmt.Errorf("trace line %d: %w", n, err)
		}
		if at < 0 {
			return fmt.Errorf("trace line %d: negative time", n)
		}
		if f[1] == "lost" {
			e.Lost()
		} else {
			ms, err := strconv.ParseFloat(f[1], 64)
			ns := ms * float64(time.Millisecond)
			if err != nil || !(ns >= 1 && ns < math.Exp2(63)) {
				return fmt.Errorf("trace line %d: bad rtt %q", n, f[1])
			}
			e.Probe(time.Duration(ns))
		}
		cost := uint64(0)
		if e.Up() {
			cost = pathcost.Edge(e.Link(), pathcost.Interactive)
		}
		if _, err := fmt.Fprintf(out, "%8d ms  up=%t rtt=%d us loss=%d ppm miss=%d cost=%d us\n", at, e.Up(), e.RTTus(), e.LossPPM(), e.Miss(), cost); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return nil
}

func flush(w *bufio.Writer, err *error) {
	if e := w.Flush(); *err == nil {
		*err = e
	}
}
