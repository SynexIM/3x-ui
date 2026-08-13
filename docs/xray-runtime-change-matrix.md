# Xray runtime change matrix

This matrix is a product promise: a process restart disconnects every customer
on the node. `internal/xray/hot_reload_matrix_test.go` keeps code and this
boundary aligned.

| Change | Restarts Xray? | Runtime mechanism |
| --- | --- | --- |
| Add/remove a non-API inbound | No | HandlerService |
| Add/remove/change a VLESS, VMess, Trojan, or Hysteria user | No | HandlerService user operation |
| Change a Mixed or HTTP account | No | Replace only that inbound handler |
| Add/remove/change a non-default outbound | No | HandlerService |
| Change routing rules or balancers | No | RoutingService |
| Add/remove/change a bridge or portal when `reverse` is already enabled | No | ReverseService |
| Enable or disable the top-level `reverse` app | Yes | Core app lifecycle is startup-only |
| Change the API inbound, API services, log, DNS, policy, stats, metrics, transport, observatory, FakeDNS, geodata, or environment | Yes | No safe runtime API |
| Change routing `domainStrategy` or the pinned bootstrap/default outbound | Yes | Startup/default selection semantics |
| Change REALITY listener/authenticator settings | Yes | Handler replacement is not reliable |

Malformed runtime-diff inputs, missing tags, and duplicate reverse tags take the
safe path and restart instead of reporting a hot apply that did not happen.
