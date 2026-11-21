package pathcost

// Hysteresis is when a cheaper alternative takes the place of the
// current choice: only once it is cheaper by GainPct for Hold
// consecutive measurement periods (an unusable current choice is
// replaced at once); cost moves below PushPct leave a published choice
// as it is. Zero fields take the defaults (Norm).
type Hysteresis struct {
	GainPct, Hold, PushPct uint64
}

// Defaults of Hysteresis.
const (
	DefaultGainPct = 20
	DefaultHold    = 3
	DefaultPushPct = 20
)

// Norm is h with its zero fields set to the defaults.
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
	return h
}

// Better reports cand cheaper than cur by h.GainPct.
func (h Hysteresis) Better(cand, cur uint64) bool { return CheaperBy(cand, cur, h.GainPct) }

// Pushed reports a cost move from o to n worth publishing (h.PushPct).
func (h Hysteresis) Pushed(n, o uint64) bool { return Moved(n, o, h.PushPct) }

// CheaperBy reports cand at least pct % below cur.
func CheaperBy(cand, cur uint64, pct uint64) bool {
	return float64(cand)*100 <= float64(cur)*float64(100-pct)
}

// Moved reports a change from o to n of pct % of o or more.
func Moved(n, o, pct uint64) bool {
	d := n - o
	if n < o {
		d = o - n
	}
	return d*100 >= o*pct
}
