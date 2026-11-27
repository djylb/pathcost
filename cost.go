package pathcost

import (
	"math"
	"math/bits"
)

// Class is a flow class: which property of a path matters. It is
// derived from the flow (ClassOf), never configured.
type Class uint8

// Flow classes. The zero value is interactive.
const (
	Interactive Class = iota
	Bulk
	// Realtime is a datagram flow: RTT matters and loss on a carrier
	// without unreliable datagrams costs more (RealtimeLossFactor).
	Realtime
)

// Reference transfers of the classes (bytes): one round of a request
// and its answer for interactive and realtime flows, a sizeable download
// for bulk.
const (
	refInteractive = 16 << 10
	refBulk        = 8 << 20
)

// BulkRateBps is the measured rate above which a TCP flow counts as bulk
// for the paths of the flows opened after it: bytes/s.
const BulkRateBps = 1 << 20

// RealtimeLossFactor multiplies the loss of an edge for realtime flows
// when its carrier has no unreliable datagrams: a lost datagram there
// is a retransmission stall, not a drop.
const RealtimeLossFactor = 4

// ClassOf is the flow class of a flow: datagrams are realtime; a stream
// is bulk once the flows of its kind were measured above BulkRateBps
// (bulk), interactive otherwise.
func ClassOf(datagram, bulk bool) Class {
	switch {
	case datagram:
		return Realtime
	case bulk:
		return Bulk
	}
	return Interactive
}

func (c Class) ref() uint64 {
	if c == Bulk {
		return refBulk
	}
	return refInteractive
}

// mss is the segment size of the Mathis bound (bytes).
const mss = 1400

// mathisC is the Mathis constant sqrt(3/2).
const mathisC = 1.22

// Exhausted is the cost a spent quota adds: larger than any path of
// edges that are not, so such a path is chosen only without another.
const Exhausted = uint64(1) << 50

// Lambda presets express the cost in microseconds per MiB of reference
// transfer for each unit of price per GiB. The numbers are initial values,
// to be calibrated on real networks.
const (
	// LambdaQuality gives the least weight to price.
	LambdaQuality = 100
	// LambdaBalanced is the default price weight.
	LambdaBalanced = 1000
	// LambdaSaving gives the most weight to price.
	LambdaSaving = 10000
)

// scarcityUsPerMiB is the exhaustion cost of one unit of scarcity
// (Progress, 1000 = 1) per MiB of the reference transfer.
const scarcityUsPerMiB = 10_000

// maxScarcity bounds the scarcity weight (in 1/1000) below exhaustion.
const maxScarcity = 64_000

// MinElapsedPPM is the minimum elapsed fraction [Progress] divides by.
// It limits the cost increase at the start of a period.
const MinElapsedPPM = 1_000_000 / 30

// Fee is the configured cost of an edge's bytes per MiB (µs): λ × the
// unit price of its uplinks (both ends) plus the scarcity of their
// quotas, and whether one is spent. FeeOf builds it; λ is applied once
// there, so a fee travels through the cost model as a time.
type Fee struct {
	UsPerMiB  uint64
	Exhausted bool
}

// FeeOf is the fee of an uplink end: λ (0: LambdaBalanced) × its unit
// price per GiB, plus its quota's scarcity (Progress), exhausted once
// the quota is spent. No unit price and a quota within its pace is 0.
func FeeOf(lambda, pricePerGiB uint64, scarcity uint32, exhausted bool) Fee {
	if lambda == 0 {
		lambda = LambdaBalanced
	}
	us := float64(lambda)*float64(pricePerGiB) + float64(scarcity)/1000*scarcityUsPerMiB
	return Fee{UsPerMiB: uint64(min(us, float64(Exhausted-1))), Exhausted: exhausted}
}

// Add is the fee of bytes paying both f and g (the two ends of an edge).
func (f Fee) Add(g Fee) Fee {
	return Fee{UsPerMiB: min(min(f.UsPerMiB, Exhausted-1)+min(g.UsPerMiB, Exhausted-1), Exhausted-1), Exhausted: f.Exhausted || g.Exhausted}
}

// FeeBand is the band of f (Band): BandExhausted when spent.
func FeeBand(f Fee) uint8 {
	if f.Exhausted {
		return BandExhausted
	}
	return Band(f.UsPerMiB)
}

// BandFee is the fee band b stands for (FeeBand's inverse).
func BandFee(b uint8) Fee {
	if b >= BandExhausted {
		return Fee{Exhausted: true}
	}
	return Fee{UsPerMiB: BandUs(b)}
}

// Progress is the scarcity of a quota: used of quota
// bytes when elapsedPPM of the period has gone. Progress used ÷ (quota ×
// elapsed) at most 1 is free (0); above, the scarcity grows with the
// overshoot of the projected use and without bound as the quota runs
// out ((p−1)/(1−used/quota), capped); a spent quota is exhausted. No
// quota (0) is free.
func Progress(used, quota uint64, elapsedPPM uint32) (scarcity uint32, exhausted bool) {
	if quota == 0 {
		return 0, false
	}
	if used >= quota {
		return 0, true
	}
	e := float64(min(max(elapsedPPM, MinElapsedPPM), 1_000_000)) / 1e6
	u := float64(used) / float64(quota)
	p := u / e
	if p <= 1 {
		return 0, false
	}
	return uint32(min((p-1)/(1-u)*1000, maxScarcity)), false
}

// Link is one edge in one direction, as measured and configured.
type Link struct {
	// RTTus is the smoothed round trip with the queueing part taken out
	// (at least the minimum RTT); 0: unknown, treated as 1 ms.
	RTTus uint32
	// LossPPM is the loss rate in parts per million (transport
	// retransmission statistics or probe loss).
	LossPPM uint32
	// RateBps is the available rate in this direction (bytes/s): the
	// throughput of actual traffic, else a budgeted probe; 0: unknown
	// (no limit from it).
	RateBps uint64
	// QueueUs is the measured queueing delay of the edge (µs): the
	// congestion term.
	QueueUs uint32
	// NoDatagram: the carrier has no unreliable datagrams (TCP-like):
	// realtime flows weigh its loss by RealtimeLossFactor.
	NoDatagram bool
	Fee        Fee
	// BiasUs is the administrator's preference added to the cost.
	BiasUs int64
}

// LossDown is the single loss threshold: a link losing half of its
// probes or more is down.
const LossDown = 500_000

// Unknown reports that nothing of l was measured.
func (l Link) Unknown() bool {
	return l.RTTus == 0 && l.LossPPM == 0 && l.RateBps == 0 && l.QueueUs == 0
}

func (l Link) effectiveRTT() uint32 {
	if l.RTTus == 0 {
		return 1000
	}
	return l.RTTus
}
func (l Link) throughput(c Class) float64 {
	t := math.MaxFloat64
	loss := min(l.LossPPM, 1_000_000)
	if c == Realtime && l.NoDatagram {
		loss = min(loss*RealtimeLossFactor, 1_000_000)
	}
	if loss > 0 {
		rtt := float64(l.effectiveRTT()) / 1e6
		p := float64(min(loss, 1_000_000)) / 1e6
		t = mss * mathisC / (rtt * math.Sqrt(p))
	}
	if l.RateBps > 0 {
		t = min(t, float64(l.RateBps))
	}
	return t
}

// Throughput is the effective throughput of l (bytes/s): the Mathis
// bound for its RTT and loss, capped by its measured rate. If both loss
// and rate are zero, it returns math.MaxFloat64.
func (l Link) Throughput() float64 { return l.throughput(Interactive) }

// Edge is the cost of l alone for class c (µs, at least 1).
func Edge(l Link, c Class) uint64 { return Path([]Link{l}, c) }

// Whole microseconds fit in a signed 128-bit sum even at the maximum
// length of a []Link, including eight times each uint64 fee for bulk.
type microseconds struct{ hi, lo uint64 }

func (s *microseconds) add(hi, lo uint64) {
	var carry uint64
	s.lo, carry = bits.Add64(s.lo, lo, 0)
	s.hi += hi + carry
}

// Path is the cost of a path of links for class c (µs, at least 1); see
// the package comment for its terms.
func Path(ls []Link, c Class) uint64 {
	if len(ls) == 0 {
		return 1
	}
	ref := c.ref()
	const mib = 1 << 20
	var total microseconds
	var remainder uint64 // fractional fee in units of 1/mib µs
	tput := math.MaxFloat64
	var extra uint64
	for _, l := range ls {
		tput = min(tput, l.throughput(c))
		// Split the fee before scaling so the fractional part is exact.
		fee := l.Fee.UsPerMiB
		total.add(bits.Mul64(fee/mib, ref))
		remainder += (fee % mib) * ref
		total.add(0, uint64(l.effectiveRTT())+uint64(l.QueueUs)+remainder/mib)
		remainder %= mib
		total.add(uint64(l.BiasUs>>63), uint64(l.BiasUs))
		if l.Fee.Exhausted {
			extra = Exhausted
		}
	}
	if tput < math.MaxFloat64 {
		whole, frac := math.Modf(float64(ref) / tput * 1e6)
		total.add(0, uint64(whole))
		if frac >= float64(mib-remainder)/mib {
			total.add(0, 1)
		}
	}
	if int64(total.hi) < 0 {
		return 1 + extra
	}
	if total.hi != 0 || total.lo >= Exhausted {
		return Exhausted - 1 + extra
	}
	return max(total.lo, 1) + extra
}

// AddBias combines two configured preferences, saturating at the signed limits.
func AddBias(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	if b < 0 && a < math.MinInt64-b {
		return math.MinInt64
	}
	return a + b
}

// Bands quantise a cost term into 6 bits for compact announcement: band
// b stands for BandUs(b), a log scale of a third of an octave from
// 100 µs; BandExhausted is a spent quota. One quantiser for every banded
// cost, so the reader turns it back alike.
const (
	BandMax       = 62
	BandExhausted = 63
)

// Band is the band of a cost term in µs (≥ Exhausted: BandExhausted).
func Band(us uint64) uint8 {
	if us >= Exhausted {
		return BandExhausted
	}
	b := math.Round(3 * math.Log2(1+float64(us)/100))
	return uint8(min(b, BandMax))
}

// BandUs is the µs band b stands for (Band's inverse on the band
// centres); BandExhausted and above is Exhausted.
func BandUs(b uint8) uint64 {
	if b >= BandExhausted {
		return Exhausted
	}
	return uint64(math.Round(100 * (math.Exp2(float64(b)/3) - 1)))
}
