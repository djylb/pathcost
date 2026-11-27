package probe

import (
	"slices"
	"testing"
	"time"
)

func TestProbeSmoothingAndLoss(t *testing.T) {
	var e Estimator
	e.Lost()
	if e.Up() || !e.Known() {
		t.Fatal("never answered: down at once")
	}
	e.Probe(8 * time.Millisecond)
	if !e.Up() || e.RTTus() != 8000 {
		t.Fatalf("first sample %d", e.RTTus())
	}
	e.Probe(16 * time.Millisecond)
	if e.RTTus() != 9000 {
		t.Fatalf("α=1/8: %d", e.RTTus())
	}
	e.Lost()
	e.Lost()
	if !e.Up() || e.Miss() != 2 {
		t.Fatal("two misses keep it up")
	}
	e.Lost()
	if e.Up() {
		t.Fatal("MissDown misses: down")
	}
	if got := e.LossPPM(); got != 4*1_000_000/6 {
		t.Fatalf("loss %d", got)
	}
	if e.ProbeTimeout(time.Second) != 36*time.Millisecond {
		t.Fatal(e.ProbeTimeout(time.Second))
	}
}

func TestSession(t *testing.T) {
	var e Estimator
	e.Lost()
	e.Session(Sample{RTT: 30 * time.Millisecond, MinRTT: 10 * time.Millisecond, QueueDelay: 25 * time.Millisecond, LossPPM: 7}, false)
	if !e.Up() || !e.Live() || e.RTTus() != 10_000 || e.QueueUs() != 20_000 || e.LossPPM() != 7 {
		t.Fatalf("%+v", e)
	}
	e.Session(Sample{RTT: time.Second}, true)
	if e.RTTus() != 10_000 {
		t.Fatal("suspended session moved the estimate")
	}
	e.Down()
	if e.Up() || e.Live() || e.LossPPM() != 0 {
		t.Fatalf("%+v", e)
	}
}

func TestReportable(t *testing.T) {
	old := Reading{Up: true, RTTus: 20_000}
	if Reportable(old, Reading{Up: true, RTTus: 23_000}) {
		t.Fatal("15 % moved")
	}
	if !Reportable(old, Reading{Up: true, RTTus: 24_000}) || !Reportable(old, Reading{RTTus: 20_000}) {
		t.Fatal("20 % or Up not moved")
	}
	if Reportable(Reading{RTTus: 1000}, Reading{RTTus: 2900}) {
		t.Fatal("below the floor")
	}
	if !Reportable(old, Reading{Up: true, RTTus: 20_000, LossPPM: 600_000}) || !Reportable(old, Reading{Up: true, RTTus: 20_000, RateInBps: 1}) {
		t.Fatal("loss crossing / rate known")
	}
	newer := Reading{Up: true, RTTus: 24_000}
	if Reportable(newer, old) {
		t.Fatal("RTT change must use the old baseline")
	}
	slow := Reading{RateOutBps: 120, RateInBps: 120}
	fast := Reading{RateOutBps: 200, RateInBps: 200}
	if RateMoved(200, 120) || !RateMoved(120, 200) || Reportable(fast, slow) ||
		!Reportable(slow, Reading{RateOutBps: 200, RateInBps: 120}) ||
		!Reportable(slow, Reading{RateOutBps: 120, RateInBps: 200}) {
		t.Fatal("rate change must use the old baseline")
	}
}

func TestMedianPreservesInput(t *testing.T) {
	rtts := []time.Duration{4, 1, 3, 2}
	if Median(rtts) != 3 || !slices.Equal(rtts, []time.Duration{4, 1, 3, 2}) {
		t.Fatalf("median changed its input: %v", rtts)
	}
	if Median(nil) != 0 || Rate(1000, time.Second/2) != 2000 {
		t.Fatal("median/rate")
	}
}
func TestSessionProbeBoundaries(t *testing.T) {
	var e Estimator
	e.Probe(-1)
	e.Probe(0)
	if e.Known() {
		t.Fatal("invalid sample")
	}
	e.Session(Sample{RTT: 30 * time.Millisecond, QueueDelay: 20 * time.Millisecond}, false)
	for range MissDown {
		e.Lost()
	}
	if !e.Up() {
		t.Fatal("live session marked down")
	}
	e.Down()
	e.Probe(1)
	if e.QueueUs() != 0 || e.RTTus() != 1 {
		t.Fatal(e.Link())
	}
	if Rate(1<<62, time.Nanosecond) != ^uint64(0) || Rate(1<<62+1, time.Second) != 1<<62+1 {
		t.Fatal("rate")
	}
}
