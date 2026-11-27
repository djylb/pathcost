package pathcost_test

import (
	"fmt"

	"github.com/djylb/pathcost"
)

// Path prices a path of measured links for a flow class, in microseconds.
// A faster two-hop path is cheaper than a slow direct link for an
// interactive flow.
func ExamplePath() {
	direct := []pathcost.Link{{RTTus: 200_000}}
	relayed := []pathcost.Link{{RTTus: 20_000}, {RTTus: 20_000}}

	fmt.Println(pathcost.Path(relayed, pathcost.Interactive) < pathcost.Path(direct, pathcost.Interactive))

	// A link in the middle of a path can limit the throughput of a bulk flow.
	narrow := []pathcost.Link{{RTTus: 20_000}, {RTTus: 20_000, RateBps: 100_000}}
	fmt.Println(pathcost.Path(narrow, pathcost.Bulk) > pathcost.Path(relayed, pathcost.Bulk))
	// Output:
	// true
	// true
}

// Hysteresis decides when a cheaper alternative replaces the current
// choice: it has to be cheaper by GainPct, and the caller holds it for Hold
// measurement periods.
func ExampleHysteresis() {
	h := pathcost.Hysteresis{GainPct: 20, Hold: 3}
	fmt.Println(h.Better(85_000, 100_000)) // 15% cheaper
	fmt.Println(h.Better(75_000, 100_000)) // 25% cheaper
	// Output:
	// false
	// true
}
