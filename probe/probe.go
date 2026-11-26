// Package probe turns measurements of a link into the estimate the cost
// model reads (pathcost.Link): probe round trips and their losses, or a
// live session's transport statistics, smoothed; and the thresholds a
// reading must cross before it is worth reporting again.
//
// Pure: the caller times the probes and passes their outcomes; no clock,
// no IO.
package probe

import (
	"math/bits"
	"slices"
	"time"

	"github.com/djylb/pathcost"
)

// Estimator constants.
const (
	// MissDown is how many probes in a row must fail for a link that
	// answered before to count as down.
	MissDown = 3
	// LossWindow is how many of the last probes the loss rate counts.
	LossWindow = 16
	// alphaShift is the smoothing of probe round trips: EWMA α = 1/8.
	alphaShift = 3
)

// Sample is what a live session's transport says of its link.
type Sample struct {
	RTT        time.Duration // smoothed by the transport; 0 before a sample
	MinRTT     time.Duration
	QueueDelay time.Duration // the transport's queueing delay estimate
	LossPPM    uint32
	RateOut    uint64 // delivery rate of actual traffic (bytes/s); 0 unknown
}

// BaseRTT is the RTT without the queueing part: the smoothed RTT less the
// queueing delay estimate, not below the minimum RTT (0: unknown).
func (s Sample) BaseRTT() time.Duration {
	if s.RTT <= 0 {
		return 0
	}
	r := s.RTT - max(s.QueueDelay, 0)
	if s.MinRTT > 0 {
		r = max(r, s.MinRTT)
	}
	return max(r, time.Microsecond)
}

// Estimator is one link's estimate. The zero value knows nothing. While
// a live session carries the link (Session) its transport statistics are
// the estimate; otherwise probe outcomes (Probe, Lost) are, the RTT
// smoothed with α = 1/8 and the loss the share lost of the last
// LossWindow probes. Not safe for concurrent use.
type Estimator struct {
	srtt     uint32 // µs
	queue    uint32 // µs, of the live session
	sessLoss uint32 // ppm, of the live session
	loss     uint16 // ring of the last probes, 1 = lost
	nloss    int
	miss     int
	known    bool
	up       bool
	live     bool
}

// Probe records a positive RTT outside a live session.
func (e *Estimator) Probe(rtt time.Duration) {
	if e.live || rtt <= 0 {
		return
	}
	e.shift()
	s := micros(rtt)
	if !e.known || e.srtt == 0 {
		e.srtt = s
	} else {
		e.srtt = uint32(int64(e.srtt) + (int64(s)-int64(e.srtt))>>alphaShift)
	}
	e.known, e.up, e.miss = true, true, 0
}

// Lost takes a probe that failed: MissDown in a row is down, and a link
// never answered is down at once.
func (e *Estimator) Lost() {
	if e.live {
		return
	}
	e.shift()
	e.loss |= 1
	e.miss++
	if e.miss >= MissDown || !e.known {
		e.up = false
	}
	e.known = true
}

func (e *Estimator) shift() {
	e.loss <<= 1
	e.nloss = min(e.nloss+1, LossWindow)
}

// Session takes a live session's statistics: the link is up, the probe
// history is cleared. A suspended session (stale is set: it lost its
// transport and resumes) keeps the last RTT, queueing delay and loss.
func (e *Estimator) Session(s Sample, stale bool) {
	e.live, e.known, e.up, e.miss, e.loss, e.nloss = true, true, true, 0, 0, 0
	if stale {
		return
	}
	if r := s.BaseRTT(); r > 0 {
		e.srtt = micros(r) // smoothed by the transport, queueing taken out
		e.queue = uint32(min(max(s.RTT-r, 0)/time.Microsecond, 1<<32-1))
	}
	e.sessLoss = s.LossPPM
}

// Down takes the end of the live session (or of the peer): the link is
// down at once and probes estimate it from now on.
func (e *Estimator) Down() { *e = Estimator{known: e.known} }

// Known reports a sample taken (a session or a probe).
func (e *Estimator) Known() bool { return e.known }

// Up reports the link usable.
func (e *Estimator) Up() bool { return e.up }

// Live reports a live session estimating the link.
func (e *Estimator) Live() bool { return e.live }

// Miss is the probes failed in a row.
func (e *Estimator) Miss() int { return e.miss }

// RTTus is the smoothed RTT without queueing (µs; 0 unknown).
func (e *Estimator) RTTus() uint32 { return e.srtt }

// QueueUs is the live session's queueing delay (µs).
func (e *Estimator) QueueUs() uint32 { return e.queue }

// LossPPM is the loss rate: the live session's, else the probes'.
func (e *Estimator) LossPPM() uint32 {
	if e.live {
		return e.sessLoss
	}
	if e.nloss == 0 {
		return 0
	}
	mask := uint16(1<<e.nloss - 1)
	return uint32(bits.OnesCount16(e.loss&mask)) * 1_000_000 / uint32(e.nloss)
}

// ProbeTimeout is the timeout of the next probe: at most limit, and 4 ×
// the smoothed RTT once known.
func (e *Estimator) ProbeTimeout(limit time.Duration) time.Duration {
	if e.srtt > 0 {
		return min(limit, 4*time.Duration(e.srtt)*time.Microsecond)
	}
	return limit
}

// Link is the estimate as a cost model link (rates are the caller's).
func (e *Estimator) Link() pathcost.Link {
	return pathcost.Link{RTTus: e.srtt, LossPPM: e.LossPPM(), QueueUs: e.queue}
}

// Median is the median of rtts (the round trip of a probe of several
// calls); 0 for none. rtts is sorted in place.
func Median(rtts []time.Duration) time.Duration {
	if len(rtts) == 0 {
		return 0
	}
	slices.Sort(rtts)
	return rtts[len(rtts)/2]
}

// Rate is the throughput of n bytes received in el (bytes/s); 0 when el
// or n is not positive. Positive rates round down and saturate at MaxUint64.
func Rate(n int64, el time.Duration) uint64 {
	if el <= 0 || n <= 0 {
		return 0
	}
	hi, lo := bits.Mul64(uint64(n), uint64(time.Second))
	if hi >= uint64(el) {
		return ^uint64(0)
	}
	q, _ := bits.Div64(hi, lo, uint64(el))
	return q
}

func micros(d time.Duration) uint32 { return uint32(min(max(d/time.Microsecond, 1), 1<<32-1)) }
