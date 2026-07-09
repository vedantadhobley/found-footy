# Rate limits + response headers

**Status: STUB — from observation + Python + our adapter code.**

**Source URL**: <https://www.api-football.com/documentation-v3> →
plans + rate limits sections.

## Per-plan quotas (from `/status` endpoint response)

Our adapter probes `/status` on init and gets back:

```json
{
  "response": {
    "subscription": { "plan": "Pro", ... },
    "requests": { "current": 316, "limit_day": 7500 }
  }
}
```

| Plan | Daily quota | Per-minute burst (observed) |
|---|---|---|
| Free | 100/day | ~10/min |
| Pro | 7500/day | ~300/min |
| Ultra | 75000/day | ? |
| Mega | ? | ? |

**Verify** — the per-minute burst numbers are inferred from
production behavior, not docs.

## Response headers

Our adapter scrapes these headers (see
`internal/infra/apifootball/client.go` `observeRateLimitHeaders`):

| Header | Semantic | Notes |
|---|---|---|
| `x-ratelimit-requests-remaining` | Requests left in the current per-minute window | Rolls over every ~60s |
| `x-rapidapi-requests-remaining` | Requests left in the daily quota | Rolls over at UTC midnight (verify) |
| `x-ratelimit-requests-limit` | The per-minute cap | Static per plan |
| `x-rapidapi-requests-limit` | The daily cap | Static per plan |

Our adapter emits Prometheus gauges from these — see
`internal/infra/apifootball/instruments.go`.

## Failure modes

### HTTP 429 (rate limited)

Our adapter treats 429 as a distinct outcome class (`rate_limited`
metric label). Body observed in production:

```json
{
  "response": [],
  "errors": { "rateLimit": "Too many requests" },
  "results": 0
}
```

**Retry-after header**: unclear whether the API sends
`Retry-After`. Our activity retry policy uses exponential backoff
independent of any Retry-After hint.

### HTTP 200 + non-empty `errors` field

Soft errors — bad parameter, quota-of-day exhausted before hard
429, etc. Python treats this as a WARN (`data.get("errors")` in
`fixtures_batch`) but still processes the `response` array. We
currently ignore the errors field. **Might be worth surfacing.**

### HTTP 5xx

Rare but happens during high-traffic periods (Champions League
nights, world cup). Retry policy handles it.

## Doc sections to paste when you have time

- Full plan-tier table (quotas, burst limits)
- Exact header names + semantics (confirm the `x-rapidapi-*` vs
  `x-ratelimit-*` distinction — is it per-plan or does the API
  send both always?)
- Retry-After semantics (does 429 include it?)
- Soft-error response body structure
