// Command standalone is a path cost simulator over pathcost: it reads a
// scenario (topology, link measurements, fees, demands, and timed
// measurement changes) as JSON and prints, per step, the K candidate
// paths of every demand with their costs per flow class and the switches
// of first paths, so hysteresis and re-routing can be watched. A second
// mode replays a probe trace through the probe estimator.
//
//	standalone [-json] scenario.json      # "-" reads stdin
//	standalone -replay trace.txt          # lines: <ms> <rtt_ms>|lost
//
// It serves as a test rig and as a calibration aid against measurements
// taken on real networks.
package main
