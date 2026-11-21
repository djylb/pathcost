package pathcost

import "math"

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

func (c Class) ref() float64 {
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

// Lambdas of three presets: quality first, balanced, saving first (µs
// per unit price per GiB per MiB of the reference transfer). The numbers
// are initial values, to be calibrated on real networks.
const (
	LambdaQuality  = 100
	LambdaBalanced = 1000
	LambdaSaving   = 10000
)

// scarcityUsPerMiB is the exhaustion cost of one unit of scarcity
// (Progress, 1000 = 1) per MiB of the reference transfer.
const scarcityUsPerMiB = 10_000

// maxScarcity bounds the scarcity weight (in 1/1000) below exhaustion.
const maxScarcity = 64_000

// MinElapsedPPM is the least elapsed share of a period Progress divides
// by: a period that has just begun does not jump the cost.
const MinElapsedPPM = 1_000_000 / 30

// Fee is the configured cost of an edge's bytes per MiB (µs): λ × the
// unit price of its uplinks (both ends) plus the scarcity of their
// quotas, and whether one is spent. FeeOf builds it; λ is applied once
// there, so a fee travels (a band, a path summary) as a time.
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
	return Fee{UsPerMiB: min(f.UsPerMiB+g.UsPerMiB, Exhausted-1), Exhausted: f.Exhausted || g.Exhausted}
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
func (l Link) Unknown() bool { return l.RTTus == 0 && l.LossPPM == 0 && l.RateBps == 0 }

func (l Link) throughput(c Class) float64 {
	t := math.MaxFloat64
	loss := l.LossPPM
	if c == Realtime && l.NoDatagram {
		loss = min(loss*RealtimeLossFactor, 1_000_000)
	}
	if loss > 0 {
		rtt := float64(max(l.RTTus, 1)) / 1e6
		p := float64(min(loss, 1_000_000)) / 1e6
		t = mss * mathisC / (rtt * math.Sqrt(p))
	}
	if l.RateBps > 0 {
		t = min(t, float64(l.RateBps))
	}
	return t
}

// Throughput is the effective throughput of l (bytes/s): the Mathis
// bound for its RTT and loss, capped by its measured rate. No loss and no
// rate is unbounded (math.MaxFloat64).
func (l Link) Throughput() float64 { return l.throughput(Interactive) }

// Edge is the cost of l alone for class c (µs, at least 1).
func Edge(l Link, c Class) uint64 { return Path([]Link{l}, c) }

// Path is the cost of a path of links for class c (µs, at least 1); see
// the package comment for its terms.
func Path(ls []Link, c Class) uint64 {
	if len(ls) == 0 {
		return 1
	}
	ref := c.ref()
	mib := ref / (1 << 20)
	var rtt, queue, fee, bias float64
	tput := math.MaxFloat64
	var extra uint64
	for _, l := range ls {
		r := l.RTTus
		if r == 0 {
			r = 1000
		}
		rtt += float64(r)
		queue += float64(l.QueueUs)
		tput = min(tput, l.throughput(c))
		fee += float64(l.Fee.UsPerMiB)
		bias += float64(l.BiasUs)
		if l.Fee.Exhausted {
			extra += Exhausted
		}
	}
	v := rtt + queue
	if tput < math.MaxFloat64 {
		v += ref / tput * 1e6
	}
	v += fee*mib + bias
	return clampAdd(v, extra)
}

// Summary is the path ls as one link: the RTTs and queueing
// delays added up, the narrowest throughput as its rate, the fees and
// biases summed, exhausted when an edge is. Path of the summary with
// further links prices the joined path as Path of all of them would for
// interactive and bulk flows (the throughput is taken without the
// realtime loss factor, a carrier's datagram support being per edge).
func Summary(ls []Link) Link {
	var s Link
	var rtt uint64
	tput := math.MaxFloat64
	for _, l := range ls {
		r := uint64(l.RTTus)
		if r == 0 {
			r = 1000
		}
		rtt += r + uint64(l.QueueUs)
		tput = min(tput, l.throughput(Interactive))
		s.Fee = s.Fee.Add(l.Fee)
		s.BiasUs += l.BiasUs
		s.NoDatagram = s.NoDatagram || l.NoDatagram
	}
	s.RTTus = uint32(min(rtt, math.MaxUint32))
	if tput < math.MaxFloat64 {
		s.RateBps = max(uint64(tput), 1)
	}
	return s
}

func clampAdd(v float64, extra uint64) uint64 {
	const lim = float64(Exhausted - 1)
	if v > lim || math.IsNaN(v) {
		v = lim
	}
	return max(uint64(max(v, 0)), 1) + min(extra, Exhausted)
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
