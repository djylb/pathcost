package pathcost

import (
	"math"
	"testing"
)

// The quality orderings: loss and a low rate cost throughput,
// interactive flows weigh RTT and bulk flows throughput.
func TestOrderings(t *testing.T) {
	clean := Link{RTTus: 10_000}
	lossy := Link{RTTus: 10_000, LossPPM: 20_000}
	if Edge(lossy, Interactive) <= Edge(clean, Interactive) || Edge(lossy, Bulk) <= Edge(clean, Bulk) {
		t.Fatal("loss costs nothing")
	}
	if Edge(clean, Interactive) != 10_000 || Edge(clean, Bulk) != 10_000 {
		t.Fatalf("a clean unlimited edge is its RTT: %d %d", Edge(clean, Interactive), Edge(clean, Bulk))
	}
	near := Link{RTTus: 5_000, RateBps: 20e6}
	far := []Link{{RTTus: 4_000, RateBps: 100e6}, {RTTus: 4_000, RateBps: 100e6}}
	if (Edge(near, Interactive) >= Path(far, Interactive)) || (Edge(near, Bulk) <= Path(far, Bulk)) {
		t.Fatalf("classes: near %d/%d far %d/%d", Edge(near, Interactive), Edge(near, Bulk), Path(far, Interactive), Path(far, Bulk))
	}
	if Path([]Link{{RTTus: 1000, RateBps: 1e6}, {RTTus: 1000}}, Bulk) != Path([]Link{{RTTus: 1000}, {RTTus: 1000, RateBps: 1e6}}, Bulk) {
		t.Fatal("order of edges matters")
	}
}

// The terms: congestion is the measured
// queueing delay; a unit price costs λ × price × reference MiB, so an
// uplink without one costs nothing and bulk pays more than interactive;
// bias adds as written and the total never drops below 1; a spent quota
// is worse than anything that is not.
func TestTerms(t *testing.T) {
	base := Link{RTTus: 10_000}
	if got := Edge(Link{RTTus: 10_000, QueueUs: 5_000}, Interactive); got != 15_000 {
		t.Fatalf("congestion: %d", got)
	}
	priced := Link{RTTus: 10_000, Fee: FeeOf(0, 2, 0, false)}
	// bulk: 8 MiB × 2 × 1000 = 16000 µs
	if got := Edge(priced, Bulk); got != 10_000+16_000 {
		t.Fatalf("bulk fee: %d", got)
	}
	// interactive: 1/64 MiB × 2 × 1000 = 31.25
	if got := Edge(priced, Interactive); got != 10_031 {
		t.Fatalf("interactive fee: %d", got)
	}
	for _, tc := range []struct{ lambda, want uint64 }{
		{LambdaQuality, 11_600},
		{LambdaBalanced, 26_000},
		{LambdaSaving, 170_000},
	} {
		if got := Edge(Link{RTTus: 10_000, Fee: FeeOf(tc.lambda, 2, 0, false)}, Bulk); got != tc.want {
			t.Errorf("bulk cost with λ=%d: got %d, want %d", tc.lambda, got, tc.want)
		}
	}
	if Edge(Link{RTTus: 10_000, Fee: Fee{}}, Bulk) != Edge(base, Bulk) {
		t.Fatal("no unit price must cost 0")
	}
	if got := Edge(Link{RTTus: 10_000, BiasUs: 2_500}, Interactive); got != 12_500 {
		t.Fatalf("bias: %d", got)
	}
	if got := Edge(Link{RTTus: 10_000, BiasUs: -50_000}, Interactive); got != 1 {
		t.Fatalf("negative bias clamps to 1: %d", got)
	}
	ex := Link{RTTus: 1, Fee: Fee{Exhausted: true}}
	if Edge(ex, Interactive) < Exhausted || Path([]Link{{RTTus: 1 << 31, LossPPM: 400_000}}, Bulk) >= Exhausted {
		t.Fatal("an exhausted edge is not last")
	}
	two := FeeOf(1, 1, 1000, false).Add(FeeOf(1, 2, 0, true))
	if two.UsPerMiB != 3+scarcityUsPerMiB || !two.Exhausted {
		t.Fatalf("fee add: %+v", two)
	}
	if s := Edge(Link{RTTus: 10_000, Fee: FeeOf(0, 0, 1000, false)}, Bulk); s != 10_000+8*scarcityUsPerMiB {
		t.Fatalf("scarcity: %d", s)
	}
	if FeeBand(Fee{Exhausted: true}) != BandExhausted || !BandFee(BandExhausted).Exhausted || BandFee(FeeBand(Fee{UsPerMiB: 2000})).UsPerMiB == 0 {
		t.Fatal("fee bands")
	}
}

// Realtime flows weigh the loss of a carrier without
// unreliable datagrams more; with datagrams they weigh it as
// interactive flows do.
func TestRealtime(t *testing.T) {
	dg := Link{RTTus: 10_000, LossPPM: 10_000}
	tcp := dg
	tcp.NoDatagram = true
	if Edge(dg, Realtime) != Edge(dg, Interactive) {
		t.Fatal("datagram carrier: realtime = interactive")
	}
	if Edge(tcp, Realtime) <= Edge(dg, Realtime) {
		t.Fatal("a TCP-like carrier must cost realtime flows more under loss")
	}
	if ClassOf(true, true) != Realtime || ClassOf(false, true) != Bulk || ClassOf(false, false) != Interactive {
		t.Fatal("ClassOf")
	}
}

// Progress: within the period's pace a quota is free;
// running ahead it grows smoothly and without bound towards the end of
// the quota; spent is exhausted; a period just begun is floored.
func TestProgress(t *testing.T) {
	const q = 1000
	if s, ex := Progress(400, q, 500_000); s != 0 || ex {
		t.Fatalf("behind the pace: %d %v", s, ex)
	}
	s1, _ := Progress(600, q, 500_000)
	s2, _ := Progress(800, q, 500_000)
	s3, _ := Progress(990, q, 500_000)
	if s1 == 0 || s2 <= s1 || s3 <= s2 {
		t.Fatalf("not rising: %d %d %d", s1, s2, s3)
	}
	if _, ex := Progress(q, q, 500_000); !ex {
		t.Fatal("spent")
	}
	if s, ex := Progress(5, q, 0); s != 0 || ex {
		t.Fatalf("a fresh period is floored at MinElapsedPPM: %d", s)
	}
	if s, ex := Progress(1<<40, 0, 1); s != 0 || ex {
		t.Fatal("no quota is free")
	}
}

// Bands are one quantiser: monotone, BandUs inverts Band on the band
// centres, and exhaustion has its own band.
func TestBands(t *testing.T) {
	prev := uint8(0)
	for us := uint64(0); us < 1<<34; us = us*2 + 1 {
		b := Band(us)
		if b < prev {
			t.Fatalf("band not monotone at %d", us)
		}
		prev = b
	}
	for b := uint8(0); b <= BandMax; b++ {
		if Band(BandUs(b)) != b {
			t.Fatalf("band %d -> %d us -> %d", b, BandUs(b), Band(BandUs(b)))
		}
	}
	if Band(Exhausted) != BandExhausted || BandUs(BandExhausted) != Exhausted {
		t.Fatal("exhausted band")
	}
}

// The switch rule: 20 % cheaper.
func TestCheaper(t *testing.T) {
	if CheaperBy(81, 100, 20) || !CheaperBy(80, 100, 20) || !CheaperBy(75, 100, 25) || CheaperBy(76, 100, 25) {
		t.Fatal("gain")
	}
}

func TestMovedUsesOldBaseline(t *testing.T) {
	if Moved(200, 120, 50) || !Moved(120, 200, 50) || !Moved(200, 100, 50) {
		t.Fatal("movement must be measured relative to the old cost")
	}
}

func TestArithmeticBoundaries(t *testing.T) {
	h := Hysteresis{}
	const high = uint64(1) << 63
	if h.Better(100, 100) || h.Pushed(100, 100) || CheaperBy(101, 100, 101) || Moved(high, high-1, 50) || !Moved(high, 0, 50) || !Moved(0, high, 50) {
		t.Fatal("percentage boundary")
	}
	links := make([]Link, 16384)
	for i := range links {
		links[i].Fee.Exhausted = true
	}
	if Path(links, Bulk) < Exhausted {
		t.Fatal("exhaustion wrapped")
	}
	if (Fee{UsPerMiB: ^uint64(0)}).Add(Fee{UsPerMiB: 1}).UsPerMiB != Exhausted-1 {
		t.Fatal("fee wrapped")
	}
	a := Link{LossPPM: 1 << 30, NoDatagram: true}
	b := a
	b.RTTus = 1000
	b.LossPPM = 1000000
	if Edge(a, Realtime) != Edge(b, Realtime) || (Link{QueueUs: 1}).Unknown() {
		t.Fatal("effective measurement")
	}
}

func TestBiasCancellation(t *testing.T) {
	const high = 1<<63 - 1
	if got := Path([]Link{{RTTus: 100, BiasUs: high}, {RTTus: 100, BiasUs: -high + 5}}, Interactive); got != 205 {
		t.Fatal(got)
	}
	if AddBias(high, high) != high || AddBias(-high-1, -high-1) != -high-1 {
		t.Fatal("bias saturation")
	}
}

func TestFeeCancellation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		class Class
		links []Link
		want  uint64
	}{
		{"single microsecond", Bulk, []Link{
			{RTTus: 100, Fee: Fee{UsPerMiB: 1 << 49}, BiasUs: -(1 << 53) - 1},
			{RTTus: 100, Fee: Fee{UsPerMiB: 1 << 49}},
		}, 199},
		{"whole word carry", Bulk, []Link{
			{RTTus: 100, Fee: Fee{UsPerMiB: 1 << 61}, BiasUs: math.MinInt64},
			{RTTus: 100, BiasUs: math.MinInt64},
		}, 200},
		{"fraction below whole microsecond", Interactive, []Link{
			{RTTus: 100, Fee: Fee{UsPerMiB: math.MaxUint64}, BiasUs: -(1 << 58)},
		}, 99},
		{"fraction carried across links", Realtime, []Link{
			{RTTus: 100, Fee: Fee{UsPerMiB: math.MaxUint64}, BiasUs: -(1 << 58)},
			{RTTus: 100, Fee: Fee{UsPerMiB: 1}},
		}, 200},
		{"fraction carried into transfer time", Interactive, []Link{
			{RTTus: 100, Fee: Fee{UsPerMiB: 32}, RateBps: 2 * refInteractive * 1_000_000},
		}, 101},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Path(tc.links, tc.class); got != tc.want {
				t.Fatalf("cost %d, want %d", got, tc.want)
			}
		})
	}
}
