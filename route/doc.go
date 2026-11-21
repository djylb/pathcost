// Package route computes candidate paths over a measured topology: per
// demand (source, destination, an optional via set one of which the path
// must cross, an optional allow set it must stay in) up to K paths — the
// cheapest for interactive flows, then each the cheapest sharing the
// fewest edges with those picked, plus the cheapest for bulk and for
// realtime flows — within a hop limit and over transit nodes only.
//
// The inputs are injected: the nodes (active, transit, bias, uplink
// fees), the node pair overrides (forbid, must, bias), the demands, both
// ends' link reports (LinkTable) and the time. Compute is pure: the
// caller owns the state and the schedule, passes the previous table and
// the edges that changed (ChangedEdge), and recomputes at the table's
// Recheck time while a switch waits out its hysteresis. A first path
// changes only once a candidate is cheaper by Topo.Switch.GainPct for
// Topo.Switch.Hold measurement periods (at once when the old one is
// unusable); smaller cost moves keep a set as it was. A path lost to
// an outage stays a recovery candidate. While a fallback is up,
// returning to the failed path requires the configured gain in probed
// RTT over the same number of consecutive fresh rounds.
//
// Compute also yields the probe lists: which node measures which peer,
// relays included when a demand pair is down, one relay per pair in turn
// while it is up.
package route
