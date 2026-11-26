# pathcost

Measurement-driven path cost and multi-hop path selection for overlay
networks, in pure Go (standard library only).

- `pathcost`: one cost function for a path of measured links and a flow
  class (interactive, bulk, realtime), in microseconds:
  `quality + congestion + λ × marginal fee + exhaustion + bias`.
  Quality is the time to deliver a reference transfer (RTTs plus the
  Mathis-bounded throughput of the narrowest link), congestion the
  measured queueing delay, the fee a per-GiB unit price weighed by λ, and
  quota scarcity rises smoothly as a quota runs ahead of its period.
  `Hysteresis` decides when a cheaper alternative replaces a choice;
  `Band` quantises costs into 6 bits.
- `route`: up to K candidate paths per demand with hop limits, transit
  nodes, via and allow constraints, edge-diverse alternatives, the
  cheapest path per flow class, hysteresis and incremental recomputation;
  also the probe lists (who measures whom, relays included).
- `probe`: link estimates from probe round trips and losses or from a
  live session's transport statistics, and report thresholds.

## Use

```go
ls := []pathcost.Link{{RTTus: 4000, RateBps: 12e6}, {RTTus: 6000, LossPPM: 1000}}
cost := pathcost.Path(ls, pathcost.Bulk)

t := &route.Topo{Nodes: nodes, Demand: route.SortDemand(demand)}
pt, changed := route.Compute(route.Inputs{Topo: t, Links: links, Now: nowMS}, prev, false)
```

Every input is injected: the topology, both ends' link reports and the
time. `Compute` is pure; the caller passes the previous table and recomputes at
`PathTable.Recheck` while a cost-based switch waits out its hysteresis.
Effective changes in either direction rebuild candidates. Pass full after a
structural change. Snapshots and all borrowed slices are read-only. To extend a
path, concatenate its Links with the next segment and call pathcost.Path.
Self demands and self loops yield no route.
For recovery from an outage, pass comparable per-link round trips with
`LinkMeas.ProbeRTTus` and `LinkMeas.ProbeAt` and recompute when they
change. Each round needs a new observation for every hop on both paths.
`PathSet.PreferFirst` carries the recovery decision to consumers that
order candidates by flow class.

## Simulator

`standalone/` is a separate module with a command-line simulator:

```
cd standalone
go run . testdata/degrade.json        # text; -json for JSON lines
go run . -replay trace.txt            # lines: <ms> <rtt_ms>|lost
```

A scenario lists nodes (transit, bias, unit prices, quota), pair
overrides (`auto`/`must`/`forbid`, bias, exhausted), link measurements,
demands (none: every pair) and timed measurement changes; the output is
the candidate paths with their costs per class and every switch of a
first path.

The scenario's `switch` object accepts `gain_pct` and `hold`. Zero selects
the default for either field. Remove `push_pct` from existing scenarios,
including `"push_pct": 0`, before running them.

Replay timestamps are nonnegative integer milliseconds. Each RTT is in
milliseconds and must be at least 1 ns (`0.000001` ms). Use `lost` for
a missing probe response.

## License

Apache-2.0
