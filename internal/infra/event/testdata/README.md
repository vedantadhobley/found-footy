# event package test goldens

Golden NATS envelopes copied **verbatim** from the `nats` repo's
`schemas/examples/` — the committed contract source. `publisher_test.go`
round-trips the Go producer structs against these to prove they serialize
to exactly the shapes the schemas define.

Keep in sync: if a subject's schema/example changes in `nats/`, re-copy
the example here and re-run `make test`.
