package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/djylb/pathcost/route"
)

func runOut(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	var b bytes.Buffer
	if err := run(args, strings.NewReader(stdin), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// A degraded direct link is left only after the hold; a recovery within
// the switch gain does not switch back.
func TestDegradeSwitchesAfterHold(t *testing.T) {
	var evs []event
	for l := range strings.SplitSeq(strings.TrimSpace(runOut(t, "", "-json", "testdata/degrade.json")), "\n") {
		var e event
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatal(err)
		}
		evs = append(evs, e)
	}
	var sw []event
	for _, e := range evs {
		if e.Kind == "switch" {
			sw = append(sw, e)
		}
	}
	if len(sw) != 1 || sw[0].AtMS != 8000 || len(sw[0].To) != 2 {
		t.Fatalf("switches %+v", sw)
	}
	last := evs[len(evs)-1]
	if last.Paths[0].Hops[0] != 2 {
		t.Fatalf("switched back within the gain: %+v", last)
	}
}

func TestAllPairsAndConstraints(t *testing.T) {
	sc := `{"nodes":[{"id":1},{"id":2,"no_transit":true},{"id":3}],
	  "pairs":[{"a":1,"b":3,"mode":"forbid"}],
	  "links":[{"a":1,"b":2,"rtt_ms":1},{"a":2,"b":3,"rtt_ms":1},{"a":1,"b":3,"rtt_ms":1}]}`
	out := runOut(t, sc, "-")
	if !strings.Contains(out, "1→3:\n             no path") || !strings.Contains(out, "1→2:\n             [2]") {
		t.Fatal(out)
	}
	if err := run([]string{"-"}, strings.NewReader(`{"nodes":[],"bogus":1}`), &bytes.Buffer{}); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestFeeAvoidedByBulk(t *testing.T) {
	sc := `{"nodes":[{"id":1},{"id":2,"price_in":10,"price_out":10},{"id":3},{"id":4}],
	  "demand":[{"src":1,"dst":3}],
	  "links":[{"a":1,"b":2,"rtt_ms":1},{"a":2,"b":3,"rtt_ms":1},{"a":1,"b":4,"rtt_ms":1.8},{"a":4,"b":3,"rtt_ms":1.8}]}`
	var e event
	if err := json.Unmarshal([]byte(runOut(t, sc, "-json", "-")), &e); err != nil {
		t.Fatal(err)
	}
	if e.Paths[0].Hops[0] != 2 || e.Paths[1].Hops[0] != 4 || e.Paths[1].BulkUs >= e.Paths[0].BulkUs {
		t.Fatalf("%+v", e.Paths)
	}
}

func TestReplay(t *testing.T) {
	out := runOut(t, "# t rtt\n0 10\n1000 lost\n2000 lost\n3000 lost\n4000 18\n", "-replay", "-")
	ls := strings.Split(strings.TrimSpace(out), "\n")
	if len(ls) != 5 || !strings.Contains(ls[3], "up=false") || !strings.Contains(ls[4], "up=true rtt=11000") {
		t.Fatal(out)
	}
	if err := run([]string{"-replay", "-"}, strings.NewReader("1 x\n"), &bytes.Buffer{}); err == nil {
		t.Fatal("bad trace accepted")
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestFinalFlushAndInvalidInputs(t *testing.T) {
	for _, tc := range []struct {
		input string
		args  []string
	}{{`{"nodes":[{"id":1},{"id":2}]}`, []string{"-"}}, {"1 1", []string{"-replay", "-"}}} {
		if err := run(tc.args, strings.NewReader(tc.input), failedWriter{}); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatal(err)
		}
	}
	for _, input := range []string{"{} {}", "{} !", `{"switch":{"push_pct":0}}`, `{"steps":[{"at_ms":-1}]}`, `{"nodes":[{"id":1},{"id":2}],"links":[{"a":1,"b":2,"loss_pct":101}]}`} {
		if err := run([]string{"-"}, strings.NewReader(input), io.Discard); err == nil {
			t.Fatal(input)
		}
	}
	for _, s := range []string{"0 NaN", "0 +Inf", "-1 1", "0 1e30"} {
		if run([]string{"-replay", "-"}, strings.NewReader(s), io.Discard) == nil {
			t.Fatal(s)
		}
	}
}
func TestMultipleFinalDeadlines(t *testing.T) {
	sc := Scenario{ProbeIntervalMS: 1000}
	for i := route.NodeID(1); i <= 6; i++ {
		sc.Nodes = append(sc.Nodes, NodeJSON{ID: i})
	}
	for i := route.NodeID(1); i <= 4; i += 3 {
		sc.Demand = append(sc.Demand, DemandJSON{Src: i, Dst: i + 2})
		sc.Links = append(sc.Links, LinkJSON{A: i, B: i + 2, RTTms: 10}, LinkJSON{A: i, B: i + 1, RTTms: 20}, LinkJSON{A: i + 1, B: i + 2, RTTms: 20})
		sc.Steps = append(sc.Steps, StepJSON{AtMS: 100 + int64(i-1)/3*100, Links: []LinkJSON{{A: i, B: i + 2, RTTms: 100}}})
	}
	switches := func() []int64 {
		var out bytes.Buffer
		if err := simulate(&sc, &out, true); err != nil {
			t.Fatal(err)
		}
		var times []int64
		dec := json.NewDecoder(&out)
		for dec.More() {
			var e event
			if err := dec.Decode(&e); err != nil {
				t.Fatal(err)
			}
			if e.Kind == "switch" {
				times = append(times, e.AtMS)
			}
		}
		return times
	}
	if got := switches(); !slices.Equal(got, []int64{3100, 3200}) {
		t.Fatal(got)
	}
	sc.Steps = append(sc.Steps, StepJSON{AtMS: 3100, Links: []LinkJSON{{A: 1, B: 2, Down: true}}})
	if got := switches(); !slices.Equal(got, []int64{3200}) {
		t.Fatal(got)
	}
}

func TestScenarioReadError(t *testing.T) {
	err := errors.New("read failed")
	for _, r := range []io.Reader{
		io.MultiReader(strings.NewReader("{} "), iotest.ErrReader(err)),
		io.MultiReader(strings.NewReader("{"+strings.Repeat(" ", 3583)), &closingBraceReader{err: err}),
	} {
		if got := run([]string{"-"}, r, io.Discard); !errors.Is(got, err) {
			t.Fatalf("got %v, want %v", got, err)
		}
	}
}

// Return the closing brace and a one-time error together, then EOF.
type closingBraceReader struct{ err error }

func (r *closingBraceReader) Read(p []byte) (int, error) {
	if r.err == nil {
		return 0, io.EOF
	}
	err := r.err
	r.err = nil
	return copy(p, "}"), err
}

func TestTraceRequiresPositiveDuration(t *testing.T) {
	for _, rtt := range []string{"0", "0.0000001"} {
		if err := replayTrace(strings.NewReader("1 lost\n2 "+rtt), io.Discard); err == nil || !strings.Contains(err.Error(), "line 2") {
			t.Fatalf("%s: %v", rtt, err)
		}
	}
}

func TestHiddenLinkChangeEmitsNoEvent(t *testing.T) {
	sc := Scenario{Nodes: []NodeJSON{{ID: 1}, {ID: 2}}, Demand: []DemandJSON{{Src: 1, Dst: 2}}, Links: []LinkJSON{{A: 1, B: 2, RTTms: 1}}, Steps: []StepJSON{{AtMS: 1, Links: []LinkJSON{{A: 1, B: 2, RTTms: 1, NoDatagram: true}}}}}
	var out bytes.Buffer
	if err := simulate(&sc, &out, true); err != nil {
		t.Fatal(err)
	}
	if bytes.Count(out.Bytes(), []byte("\n")) != 1 {
		t.Fatal(out.String())
	}
}
