# Issue closures — 2026-08-25

This snapshot removes completed validation work from the active issue register.
It preserves the production evidence and the remaining architectural
consequences.

## FF-058 — fixture-independent Twitter maintenance

Release `e2143ac` added the six-hour `TwitterMaintenanceWorkflow` on
2026-08-24. Five scheduled executions then passed naturally at 12:17 and 18:17
UTC on August 24 and 00:17, 06:17, and 12:17 UTC on August 25. Each execution
forced authentication verification and cookie persistence, rendered four
initial articles, parsed 13 or 14 tweets, found 10 through 14 video tweets, and
validated their status URLs. The schedule therefore detects cookie, DOM,
search-feed, selector, and status-URL failures without waiting for a fixture.

Full credential expiry still belongs to FF-059's raw-Firefox recovery path.
FF-058 is closed.

## FF-061 — unavailable Twitter responses consumed usable searches

Release `e2143ac` separated usable searches from unavailable probes and
persisted bounded, secret-free SearchTimeline evidence. A natural production
burst on 2026-08-24 supplied the missing proof: 18 probes from 20:16:49 through
20:35:15 UTC returned `upstream_error`, HTTP 429, limit 50, remaining 0, and
reset epochs corresponding to 20:19:48 and 20:35:11 UTC. The burst crossed
unrelated Fulham, Chelsea, Schalke, Malaga, and Roma queries. That rules out
query-specific emptiness and is consistent with the shared account/IP timeline
bucket.

All affected event workflows still completed 15 usable searches. The 18
unavailable probes were durable, separately bounded observations and consumed
no logical attempt. Loki's 3,197 successful searches reconciled exactly to
3,192 event attempts plus five maintenance canaries. The 18 classified
failures reconcile to the unavailable metadata. See the full
[incident record](../incidents/2026-08-20-twitter-feed-suppression.md).

FF-061 is closed. The measured account/IP search budget is now input to
FF-038's eventual atomic fleet controller; it is not a reason to add an
independent guessed limiter.
