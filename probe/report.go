package probe

import "github.com/djylb/pathcost"

// Report thresholds: a reading is worth reporting again when its RTT or
// queueing delay moved max(ReportRTTPct %, ReportFloorUs), its loss
// crossed pathcost.LossDown, or a rate moved ReportRatePct % (or became
// known or unknown). An Up change is always worth it.
const (
	ReportRTTPct  = 20
	ReportFloorUs = 2000
	ReportRatePct = 50
)

// Reading is one link estimate as reported.
type Reading struct {
	Up                    bool
	RTTus, LossPPM        uint32
	QueueUs               uint32
	RateOutBps, RateInBps uint64
}

// Moved reports n worth reporting after o (the report thresholds).
func Moved(o, n Reading) bool {
	return o.Up != n.Up || delayMoved(n.RTTus, o.RTTus) || delayMoved(n.QueueUs, o.QueueUs) ||
		(o.LossPPM >= pathcost.LossDown) != (n.LossPPM >= pathcost.LossDown) ||
		RateMoved(n.RateOutBps, o.RateOutBps) || RateMoved(n.RateInBps, o.RateInBps)
}

func delayMoved(n, o uint32) bool {
	d := int64(n) - int64(o)
	if d < 0 {
		d = -d
	}
	return d >= max(int64(o)*ReportRTTPct/100, ReportFloorUs)
}

// RateMoved reports a rate change worth a report: known ↔ unknown, or a
// move of ReportRatePct.
func RateMoved(n, o uint64) bool {
	if (n == 0) != (o == 0) {
		return true
	}
	return o > 0 && pathcost.Moved(n, o, ReportRatePct)
}
