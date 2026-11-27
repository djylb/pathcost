package pathcost

import "math/bits"

// Hysteresis is when a cheaper alternative takes the place of the
// current choice: only once it is cheaper by GainPct for Hold
// consecutive measurement periods (an unusable current choice is
// replaced at once). Callers can use Pushed to throttle publication. Zero fields take the defaults (Norm).
type Hysteresis struct {
	GainPct, Hold, PushPct uint64
}

// Defaults of Hysteresis.
const (
	DefaultGainPct = 20
	DefaultHold    = 3
	DefaultPushPct = 20
)

// Norm returns a copy with defaults filled in and percentages limited to 100.
func (h Hysteresis) Norm() Hysteresis {
	if h.GainPct == 0 {
		h.GainPct = DefaultGainPct
	}
	if h.Hold == 0 {
		h.Hold = DefaultHold
	}
	if h.PushPct == 0 {
		h.PushPct = DefaultPushPct
	}
	h.GainPct = min(h.GainPct, 100)
	h.PushPct = min(h.PushPct, 100)
	return h
}

// Better reports cand cheaper than cur by h.GainPct.
func (h Hysteresis) Better(cand, cur uint64) bool { return CheaperBy(cand, cur, h.Norm().GainPct) }

// Pushed reports a cost move from old to new worth publishing (h.PushPct).
func (h Hysteresis) Pushed(new, old uint64) bool { return Moved(old, new, h.Norm().PushPct) }

// CheaperBy reports cand at least pct % below cur.
func CheaperBy(cand, cur uint64, pct uint64) bool {
	return cand < cur && Moved(cur, cand, pct)
}

// Moved reports a nonzero change from old to new of at least min(pct, 100) percent.
// Every change from a zero baseline qualifies.
func Moved(old, new, pct uint64) bool {
	d := new - old
	if new < old {
		d = old - new
	}
	a, b := bits.Mul64(d, 100)
	c, e := bits.Mul64(old, min(pct, 100))
	return d != 0 && (a > c || a == c && b >= e)
}
