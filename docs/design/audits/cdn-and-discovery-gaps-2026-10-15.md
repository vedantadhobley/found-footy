# CDN denial and discovery gaps — 2026-10-15

Read-only production investigation after the
[vision-queue rollout](../../history/vision-queue-rollout-2026-10-15.md).
No application code, production configuration, candidate state, or workflow
changed. Public HTTP probes used no cookies, account search, or inference.

## Brunner 3′: explicit geographic media denial

Fixture `1636401`, CSKA Sofia–Monaco; event
`0511564b-81e1-485e-9121-71e63c5a8c6c`. Fifteen usable searches completed with
zero unavailable probes. Twelve candidate posts produced seven exhausted
`cdn_download/forbidden` failures and five deterministic content/shape rejects.
No clip was promoted.

Fresh syndication metadata returned HTTP 200 for all seven failed posts. They
referenced only two provider media objects:

| Source | Provider media ID | Candidate posts | Reported shape/duration |
|---|---|---:|---|
| CANAL+ | `2110776133876588544` | 6 | 1920×1080, 21.360 s |
| TNT Sports | `2110776871260422144` | 1 | 1920×1080, 32.362 s |

The [CANAL+ post](https://x.com/CanalplusFoot/status/2110776182803230941)
and [TNT post](https://x.com/footballontnt/status/2110776950461489192)
explicitly describe Brunner's opening goal. This is not an unrelated-query
sample or seven independently uploaded media objects. Provider identity is
not a computed MD5: no failed media bytes were available to hash.

For both objects, the highest and lowest offered MP4 qualities and HLS master
returned HTTP 403. Ordinary GETs, byte-range GETs, the application's Twitter
Referer/Origin, X Referer/Origin, and X Referer without Origin did not recover
the tested MP4. Multiple ordinary responses returned the exact 13-byte plain
text `Geo Forbidden`; some denials carried no body. The metadata exposed no
explicit country/availability flag.

An accepted Franculino control post `2110806152112189871`, also on the
`amplify_video` path, returned MP4 HTTP 200/206 and HLS master/child HTTP 200
through the same container and headers. Responses were canceled after bounded
ranges/chunks/manifests; no full video was downloaded or stored.

This establishes an explicit geographic-denial response for these two objects
from Found Footy's egress. It does not establish the country policy, normal
logged-in browser behavior, or the cause of every historical CDN 403. Neither
`amplify_video` nor an official uploader alone is a reliable denial classifier.

**FF-089 follow-up:** distinguish this explicit response from generic transient
403s before retry/recovery changes. Preserve bounded diagnostic evidence;
do not retain raw URLs, headers or arbitrary response bodies. More retries,
lower bitrate and HLS did not recover these samples. Shared provider-object
failure attribution could avoid repeating identical downloads across posts,
but requires a separate reviewed contract and would not make a blocked object
accessible. No such cache or source-identity implementation landed here.

## Keita 88′: no eligible candidate reached validation

Fixture `1636328`, Union St. Gilloise–Real Sociedad; event
`58ae9c17-67cd-47b6-aa93-e2ebf944a47f`. First seen `18:32:00Z`; discovery ran
from `18:33:00Z` to `19:01:10Z`. It completed fifteen usable searches plus
eleven unavailable probes, ending `candidates_no_assets`. Public API confirmed
zero videos, with no pending candidate, CDN failure, or OCR failure.

The recorded query was:

```text
(sekou OR abrahame OR keita OR "Union St. Gilloise" OR USG) filter:videos
```

Only three candidate posts were found:

- `FUTGOL360_` posts `2110801155924066793` and `2110800396989939719` advertised
  Turrientes' earlier 46′ goal. Fresh metadata reported three-second,
  1080×1350 videos. Both were rejected for short duration before download.
- [FOLINTV post `2110806800484471284`](https://x.com/FOLINTV/status/2110806800484471284)
  mentioned Keita's equalizer in a match summary. Metadata reported about
  17.2 seconds at 1080×1400. The current aspect gate rejected it before
  download. Its actual footage was not reviewed, so there is no evidence yet
  that relaxing the gate would recover a legitimate goal clip.

This is a discovery/eligibility gap, not the Brunner CDN failure. Fifteen usable
observations do not prove that no suitable clip exists on X: query semantics,
index visibility, scan limits and intervening outages still affect coverage.

## Shared search allowance remains separate

Later October 15 checklists retained HTTP 429, limit 50, remaining zero, with
shared reset epochs. Al Hamlawi 59′, Ahoka 67′ and Ramos 72′ exhausted fifteen
unavailable probes after only fourteen, twelve and eleven usable searches.
Zwarts 76′ also closed without assets after eleven usable searches and fifteen
unavailable probes. This is FF-038's known shared-account budget gap, not OCR.

At the later SQL snapshot, 1,589 post-rollout candidates had zero terminal
vision failures and 307 download failures. Those are candidate outcomes, not
1,589 model calls or 307 unique inaccessible media objects. The earlier
11-of-12 public-goal result was a first-half snapshot, not a whole-day claim.
