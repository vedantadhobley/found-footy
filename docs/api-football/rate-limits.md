# Rate limits + response headers

**Status: partially seeded from vendor doc v3.9.3, 2026-07-09.**

**Source**: `docs/api-football/vendor/api-football-v3.9.3.{pdf,html}`
→ intro / authentication section (PDF page 3). Live URL:
<https://www.api-football.com/documentation-v3> (Cloudflare-blocked
to agents).

**Not yet extracted from doc**: per-plan quota table (Free / Pro /
Ultra / Mega). The intro doc pages don't tabulate quotas by tier;
those live in the pricing/subscription section on the site, which
may not be present in this PDF export. We have the shape from our
own `/status` probes — see below.

## Response headers (doc page 3, verbatim)

The API tags every response with rate-limit headers:

| Header (case-insensitive) | Meaning                                              |
|---|---|
| `x-ratelimit-requests-limit`     | **Daily** quota for the current plan                  |
| `x-ratelimit-requests-remaining` | **Daily** requests remaining                          |
| `X-RateLimit-Limit`              | **Per-minute** burst cap                              |
| `X-RateLimit-Remaining`          | **Per-minute** requests remaining                     |

> ⚠ **Two distinct time windows, similar names.** Lowercase-with-hyphens
> `x-ratelimit-requests-*` is DAILY. Mixed-case `X-RateLimit-*` (no
> "requests" segment) is PER-MINUTE. Watch for this when writing
> observability code — our adapter's `observeRateLimitHeaders`
> should read both.

Our adapter's earlier version of this file mislabeled these as
`x-rapidapi-*` (a different vendor's convention). That was
inference from generic REST-API patterns, not this doc. Fixed now.

## Rate Limiting Policy (doc page 3, verbatim)

> *"You should not exceed the number of calls allowed for your
> subscription. Excess traffic may be temporarily or permanently
> blocked without notice."*

Key implication: **there's no documented "clean" HTTP code for
over-quota.** The vendor reserves the right to block silently.
See "Observed 429 behavior" below for what we actually see.

## Documented HTTP response codes

Per the /fixtures section (page 61), the endpoint documents:

| Code | Meaning              |
|---|---|
| 200 | OK                    |
| 204 | No Content            |
| 499 | Time Out              |
| 500 | Internal Server Error |

**429 is NOT in the documented response set.** Our earlier version
of this file claimed 429 was documented — it isn't. What we see
below is observed prod behavior.

## Observed 429 behavior

Our adapter (`internal/infra/apifootball/client.go`) treats HTTP
429 as a distinct outcome class (`rate_limited` metric label).
Observed body:

```json
{
  "response": [],
  "errors": { "rateLimit": "Too many requests" },
  "results": 0
}
```

**Retry-After header**: not yet confirmed. Our activity retry
policy uses exponential backoff independent of any Retry-After
hint the response may carry.

## Observed HTTP 200 + non-empty `errors`

Soft errors — bad parameter, over-quota-degraded-but-not-hard-blocked,
etc. Python treats this as a WARN (`data.get("errors")` in
`fixtures_batch`) but still processes the `response` array. Our Go
adapter currently ignores the `errors` field. **Might be worth
surfacing** — a soft-quota warning is a leading indicator that
we're near the hard limit.

## Observed HTTP 5xx

Rare but happens during high-traffic periods (Champions League
nights, world cup finals). Retry policy handles it.

## Per-plan quotas — from `/status` probes only

The doc export we have doesn't include the pricing/quotas page.
What we know from adapter probes:

```json
// GET /status response.response.requests
{ "current": 316, "limit_day": 7500 }
```

| Plan | Daily quota | Per-minute burst |
|---|---|---|
| Free  | 100        | ~10 (inferred)     |
| Pro   | 7500       | ~300 (observed)    |
| Ultra | 75000      | ?                  |
| Mega  | ?          | ?                  |

**These come from the pricing page, not this API doc.** Verify
against the live site if quota planning matters.

## Doc sections still to paste

- Per-plan quota table (from the pricing page, not this doc export)
- Retry-After semantics (does the API send one?)
- Any documented "soft warning" for approaching quota
