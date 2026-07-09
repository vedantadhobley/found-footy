# api-football / api-sports.io — frozen reference

**Why this exists.** The vendor docs at
<https://www.api-football.com/documentation-v3> are behind a Cloudflare
bot challenge. No agent tool (WebFetch, curl+UA, etc.) can bypass the
JS challenge — verified 2026-07-09. Rather than repeatedly rediscover
the API's behavior from Python code + guesswork, we mirror the docs
here.

## Layout

```
docs/api-football/
├── README.md                (this file)
├── events-shape.md          per-fixture events array
├── fixtures-endpoint.md     /fixtures + envelope + query params
├── status-codes.md          fixture status short codes (NS, 1H, HT, FT, PST, ...)
├── rate-limits.md           headers, quotas, retry semantics
├── examples/                real captured API responses (JSON)
└── vendor/                  raw vendor-doc archive — do NOT edit
    ├── api-football-v3.9.3.pdf           131-page PDF export
    ├── api-football-v3.9.3.html          browser-save HTML mirror
    └── api-football-v3.9.3_files/        assets (gitignored — 5MB of screenshots)
```

## Source-of-truth precedence

When files disagree, higher-numbered sources win:

1. `vendor/` — the vendor's own doc export. Authoritative for API
   contract as of the archive date.
2. This directory's `.md` files — human-distilled from `vendor/`.
   Faster to read; may lag if the vendor site updates between
   archives.
3. `archive/src/utils/event_config.py` — Python's accumulated
   wisdom, frozen at Python's last update. Historically load-bearing
   (the "Red card" casing came from here); now superseded by `vendor/`.
4. `internal/infra/apifootball/*.go` — what our Go adapter actually
   sends + parses. Observed behavior; may not match doc.
5. `examples/` — live captured API responses. Ground truth for what
   the API actually returned at capture time.

## Human update flow

When the vendor updates the API:

1. Open <https://www.api-football.com/documentation-v3> in a browser.
2. **File → Save Page As → Web Page, Complete** → download
   `API-Football - Documentation.html` + `_files/` dir.
   Also **Print → Save as PDF** for a searchable archive.
3. Drop the artifacts into this repo's root or somewhere convenient.
4. Move + rename:
   ```
   mv "API-Football - Documentation.pdf"   \
      "docs/api-football/vendor/api-football-v<X.Y.Z>.pdf"
   mv "API-Football - Documentation.html"  \
      "docs/api-football/vendor/api-football-v<X.Y.Z>.html"
   mv "API-Football - Documentation_files" \
      "docs/api-football/vendor/api-football-v<X.Y.Z>_files"
   ```
5. Fix the HTML's asset references (browser save hardcodes the
   original dir name, URL-encoded):
   ```
   sed -i 's|API-Football%20-%20Documentation_files|api-football-v<X.Y.Z>_files|g' \
       "docs/api-football/vendor/api-football-v<X.Y.Z>.html"
   ```
6. Reconcile the seeded `.md` files against the new archive. Update
   the `Status:` line at the top of each with the new date.
7. Log the version bump in [../decisions.md](../decisions.md).

The current archive is **v3.9.3** captured **2026-07-09**.

## Files

| File | Covers | Seeded from vendor |
|---|---|---|
| [events-shape.md](./events-shape.md) | Per-fixture events array — types + details + comments | ✓ 2026-07-09 (PDF p. 69) |
| [fixtures-endpoint.md](./fixtures-endpoint.md) | `/fixtures` + `/fixtures?ids=` + response envelope | ✓ 2026-07-09 (PDF pp. 58-62) |
| [status-codes.md](./status-codes.md) | Fixture status short codes (NS, 1H, HT, FT, PST, ...) | ✓ 2026-07-09 (PDF pp. 58-59) |
| [rate-limits.md](./rate-limits.md) | Rate-limit headers, quotas, 429 body | ⚠ partial 2026-07-09 (headers only; plans table not in this export) |
| [examples/](./examples/) | Real captured API responses (JSON) | as-needed |

## When agents can't find a value here

If an agent needs a field/value that's NOT in these seeded files:

1. **Grep the vendor archive first.** The HTML mirror is text-heavy
   and searchable:
   ```
   grep -i 'red card' docs/api-football/vendor/api-football-v3.9.3.html
   ```
   Or open the PDF via Read with a page range.
2. **If found in vendor**, update the corresponding seeded `.md`
   with the finding — don't just answer from the archive silently.
3. **If NOT found in vendor**, say so explicitly: "the vendor
   archive doesn't cover X; I'm inferring from Python / prod
   observation." Do NOT silently guess casing / enum values — the
   whole point of this directory is to avoid that failure mode.
