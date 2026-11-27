package route_test

import (
	"fmt"

	"github.com/djylb/pathcost/route"
)

// Compute picks candidate paths for each demand from both ends' link
// reports. The direct link between nodes 1 and 3 is slow, so the first
// candidate relays through node 2. The caller injects the time and passes
// the previous table (nil at first) to the next call.
func ExampleCompute() {
	const now = 1_000_000 // monotonic milliseconds
	nodes := map[route.NodeID]route.Node{}
	for id := route.NodeID(1); id <= 3; id++ {
		nodes[id] = route.Node{ID: id, Active: true, Transit: true}
	}
	topo := &route.Topo{
		Nodes:  nodes,
		Demand: route.SortDemand([]route.Demand{{Src: 1, Dst: 3}}),
	}

	// Both ends report each link: [0] from the lower node, [1] from the higher.
	report := func(rttUs uint32) [2]route.LinkMeas {
		m := route.LinkMeas{At: now, Up: true, RTTus: rttUs}
		return [2]route.LinkMeas{m, m}
	}
	links := route.LinkTable{
		route.MakeEdge(1, 3): report(200_000),
		route.MakeEdge(1, 2): report(20_000),
		route.MakeEdge(2, 3): report(20_000),
	}

	table, _ := route.Compute(route.Inputs{Topo: topo, Links: links, Now: now}, nil, false)
	set, ok := table.Set(1, 3, nil)
	fmt.Println(ok, len(set.Paths) >= 2)
	fmt.Println(set.Paths[0].Hops)
	fmt.Println(set.Paths[1].Hops)
	// Output:
	// true true
	// [2 3]
	// [3]
}
