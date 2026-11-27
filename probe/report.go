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

// Reportable reports whether new is worth reporting after old under the report thresholds.
func Reportable(old, new Reading) bool {
	return old.Up != new.Up || delayMoved(old.RTTus, new.RTTus) || delayMoved(old.QueueUs, new.QueueUs) ||
		(old.LossPPM >= pathcost.LossDown) != (new.LossPPM >= pathcost.LossDown) ||
		RateMoved(old.RateOutBps, new.RateOutBps) || RateMoved(old.RateInBps, new.RateInBps)
}

func delayMoved(old, new uint32) bool {
	d := int64(new) - int64(old)
	if d < 0 {
		d = -d
	}
	return d >= ReportFloorUs && pathcost.Moved(uint64(old), uint64(new), ReportRTTPct)
}

// RateMoved reports whether the rate changed from known to unknown or
// vice versa, or moved by at least ReportRatePct relative to old.
// A zero rate is unknown.
func RateMoved(old, new uint64) bool {
	return pathcost.Moved(old, new, ReportRatePct)
}
