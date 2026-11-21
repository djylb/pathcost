package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
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
	for _, l := range strings.Split(strings.TrimSpace(runOut(t, "", "-json", "testdata/degrade.json")), "\n") {
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
	if len(sw) != 1 || sw[0].AtMS != 8001 || len(sw[0].To) != 2 {
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
