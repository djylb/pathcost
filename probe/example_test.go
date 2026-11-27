package probe_test

import (
	"fmt"
	"time"

	"github.com/djylb/pathcost/probe"
)

// An Estimator turns probe round trips and losses into a link estimate
// that pathcost and route consume.
func ExampleEstimator() {
	var e probe.Estimator
	e.Probe(20 * time.Millisecond)
	e.Probe(20 * time.Millisecond)
	fmt.Println(e.Known(), e.Up(), e.RTTus())

	// Probes that never come back take the link down.
	for range probe.MissDown {
		e.Lost()
	}
	fmt.Println(e.Up())

	l := e.Link()
	fmt.Println(l.RTTus == e.RTTus(), l.LossPPM == e.LossPPM())
	// Output:
	// true true 20000
	// false
	// true true
}

// Median summarises a burst of probe round trips.
func ExampleMedian() {
	rtts := []time.Duration{30 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond}
	fmt.Println(probe.Median(rtts))
	// Output:
	// 20ms
}
