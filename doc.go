// Package pathcost is a link cost model for measurement-driven path
// selection: every edge of a network — a node pair, or a client and a
// node, over one transport — has measured properties (queueing-adjusted
// RTT, loss, per-direction available rate, queueing delay) and
// configured ones (the marginal fee of its uplinks, their quota
// scarcity, an administrator's bias), and one function turns a path of
// edges into a cost for a flow class.
//
// The cost is a time in microseconds:
//
//		quality + congestion + λ × marginal fee + exhaustion + bias
//
//	  - quality: the time to deliver the class's reference transfer — the
//	    RTTs add up, the throughput is the narrowest edge's (the Mathis
//	    bound of its RTT and loss, capped by its measured rate);
//	  - congestion: the measured queueing delay of the edges (the
//	    saturation signal; no nominal bandwidth is configured);
//	  - marginal fee: the unit price of the bytes over the path (per GiB,
//	    both ends) for the reference transfer, weighed by λ (FeeOf); an
//	    uplink without a unit price costs 0;
//	  - exhaustion: an uplink quota running ahead of its period rises
//	    smoothly (Progress, carried in the same Fee), a spent one adds
//	    Exhausted;
//	  - bias: the administrator's preference, clamped so the total stays at
//	    least 1.
//
// Hysteresis says when a cheaper alternative replaces a current choice.
//
// Subpackages: route computes K candidate paths per demand over a
// measured topology with hop limits, via and allow constraints,
// hysteresis and incremental recomputation; probe turns probe outcomes
// and transport statistics into link estimates. The standalone module
// is a command-line simulator over both.
//
// Pure: no clock, no IO; standard library only.
package pathcost
