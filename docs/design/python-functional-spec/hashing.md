# Video download and perceptual hashing — Python behavior spec

Frozen WHAT-and-WHY detail from the
[Python functional-spec index](./README.md).

## Video download + perceptual hashing — Python behavior spec (WHAT + WHY)

Files referenced: `archive/src/activities/{download.py, hashing.py}`, `archive/src/utils/{dedup_match.py, config.py}`.

### 1. Metadata pre-filter chain

**PURPOSE.** Reject unsuitable clips using cheap metadata checks before spending bandwidth or CPU on download, hashing, or vision.

**BEHAVIOR.**
- Pre-download stage reads `mediaDetails[].original_info` from the syndication response (width/height only — no duration/framerate yet), `download.py:358-390`.
- Short-edge gate: `min(width, height) >= MIN_SHORT_EDGE = 600 px` when `SHORT_EDGE_FILTER_ENABLED = True`, `config.py:57-58`. Bar tuned to allow letterboxed 720p (1280×686), not just clean 720p.
- Aspect ratio gate: `width/height` must lie in `[MIN_ASPECT_RATIO=1.75, MAX_ASPECT_RATIO=1.82]`, `config.py:68-69`, straddling 16:9=1.7778 with encoder slop.
- Pre-download filter order is short-edge, then aspect — either failure returns `status="filtered"` without spending a byte, `download.py:371-390`.
- Post-download re-verifies the same properties from ffprobe in case syndication metadata disagreed with the actual file, `download.py:601-625`.
- Duration gate runs only post-download: `MIN_VIDEO_DURATION=3.0 s` (strict; exactly 3.00s fails), `MAX_VIDEO_DURATION=90.0 s`, `download.py:585-599`, `config.py:72-73`.
- Post-download order: duration → short-edge → aspect. Any failure deletes the file and returns `None`, `download.py:585-625`.
- No framerate check at any stage.

**REMARKS.** Config comments at `config.py:60-66` cite the source-of-truth for the aspect band: prod S3 distribution 2026-06-30, 81% in 1.78-1.79, widened to 1.75-1.82 to absorb padding (1280×722=1.7729, 1280×705=1.8156) without admitting 16:10 letterboxed broadcasts (~1.60-1.72) or cinema clips (≥1.85). Phone-TV recordings pass this filter and are removed by AI vision downstream — bounds intentionally lenient. The `duration <= MIN` (not `<`) at `download.py:585` is deliberate; the comment at `:584` calls it out. The two-stage filter (metadata + ffprobe) exists because syndication `original_info` is sometimes wrong or missing. Framerate is not gated because Twitter has already normalized clips through re-encode by the time we see them.

### 2. Download flow

**PURPOSE.** Fetch the highest-bitrate MP4 variant from Twitter's public CDN without hitting rate-limited authenticated APIs.

**BEHAVIOR.**
- Extract `tweet_id` from URL via `/status/(\d+)` regex, `download.py:230-242`.
- Reject truncated Snowflakes (< 18 digits) up front with `VideoMalformedURLError`, `failure_mode=truncated_snowflake`, `download.py:256-273`.
- Call `cdn.syndication.twimg.com/tweet-result?id=<id>&token=x` with a browser UA + Twitter Referer, 5 s timeout, no auth, `download.py:276-289`.
- Variant path preferred: `mediaDetails[].video_info.variants` (has bitrate); fallback: `video.variants`, `download.py:356-394`.
- Sort MP4-only variants by bitrate desc and pick head, `download.py:402-414`.
- CDN stream download, 60 s timeout, 8 KB chunks; cookies (`auth_token`, `ct0`, `twid`, `guest_id`) attached so `amplify_video` variants can succeed, `download.py:430-487`.
- Files < 1 KB are deleted and re-raise `RuntimeError` for Temporal retry, `download.py:520-525`.
- Errors are classified into typed exceptions: `VideoNotAvailableError` (404), `VideoGeoRestrictedError` (403), `TwitterRateLimitedError` (429), `VideoCDNTimeoutError` (timeout), `VideoDownloadError` (generic), `download.py:291-321, 454-483`.

**REMARKS.** The 5 s syndication timeout is deliberate — comment at `download.py:286-287`: "if syndication API doesn't respond quickly, it's probably going to fail. Longer timeouts just delay the inevitable retry." Cookies are module-cached (`_twitter_cookies_cache`, `download.py:34-81`); only cookie *presence* is logged, values redacted. Partial failure is surfaced as an exception raise; retry policy lives in the DownloadWorkflow config and Temporal handles backoff. The rebuild should keep the typed error taxonomy — it is what makes "was the fixture blocked by geo or by rate limit?" answerable in Grafana without regex-scraping messages.

### 3. Full-video MD5 hash

**PURPOSE.** Provide a byte-identical dedup key so exact-duplicate re-uploads collapse without ever running perceptual comparison.

**BEHAVIOR.**
- MD5 computed with 4 KB read chunks over the whole file, returned hex, `download.py:658-664`.
- Runs on every downloaded clip that survives basic filters, stored as `file_hash` on the returned dict, `download.py:628, 645`.
- Batch dedup by MD5: within one download batch, candidates that share `file_hash` are collapsed to a single upload; popularity (source-URL count) fans in — the surviving candidate carries the aggregate. This happens downstream in the upload activity, not in `download.py` itself.
- `file_hash` is one of two dedup axes; the other is the perceptual hash.

**REMARKS.** MD5 is a dedup key, not a security guarantee — collisions on real re-encoded video bytes are effectively zero at this scale. 4 KB read chunking is a Python-idiom holdover; Go can safely use 64 KB+ with no behavior change. Popularity fan-in on shared MD5 is load-bearing for ranking — "seen from 8 tweets" beats "seen from 1 tweet" downstream. Do not silently discard the count on collapse. The download activity itself does not consult existing S3 for MD5; that check happens further downstream via the S3-key convention.

### 4. S3 filename convention

**PURPOSE.** Encode the MD5 in the S3 object key so a single `HEAD` answers "already in the corpus?" without a separate index.

**BEHAVIOR.**
- Local temp filename during download is `{event_id}_{video_index}_01.mp4`, `download.py:427` — that name is not the S3 key.
- The S3 key composition (in the upload activity, extracted P3b per `dedup_match.py:1-14`) includes the MD5, letting one `head_object` per candidate hash answer existence.
- The bucket listing IS the index. No sidecar catalog.

**REMARKS.** Textbook content-addressable-storage-on-S3; the rebuild should keep this shape and consider strengthening from MD5 to SHA-256 while preserving the "filename encodes hash" property.

### 5. Perceptual hash generation

**PURPOSE.** Produce a per-frame fingerprint dense enough to catch the same goal captured with different start offsets or minor re-encodes.

**BEHAVIOR.**
- Algorithm: dHash on a 9×8, grayscale, histogram-equalized frame, `hashing.py:96-108`. 9×8=72 pixels, adjacent-pixel compare per row → 8×8=64 bits.
- Histogram equalization normalizes contrast/brightness before resize so color-graded uploads don't drift the hash, `hashing.py:97`; the comment at `:47-48` calls out "handles color grading differences."
- Sample every 0.25 s starting at `t=0.25`, stop at `duration - 0.3 s` to avoid EOF glitches, `hashing.py:64, 124`.
- One fresh `ffmpeg -ss <ts> -vframes 1 -f image2pipe -vcodec png` subprocess per frame, 10 s timeout each, `hashing.py:75-89`.
- Storage: `dense:<interval>:<ts1>=<hash1>,<ts2>=<hash2>,...` with hex16 hashes, `hashing.py:49, 145`.
- Fallback: if the loop produces zero frames, try one frame at `t=1.0 s`, `hashing.py:135-141`.
- Heartbeats fire before every ffmpeg call so long clips don't fail Temporal timeout under contention, `hashing.py:67-70, 126-127`.
- Only invoked AFTER AI validation confirms soccer content, `download.py:559-560`, `hashing.py:153`.

**REMARKS.** The dense text format is Python-convenient (one `split`) but expensive to parse in Go: `strings.Split` allocates, every hex needs `strconv.ParseUint`, and the matcher is O(N²) offsets over the parsed structure. Keep the string form as the wire/DB representation for backward-compat with existing S3-corpus hashes, but parse once into an in-memory `[]struct{ts float32; hash uint64}` — an order of magnitude faster on the hot loop. The single-frame fallback at `hashing.py:135-141` is effectively dead code under `MIN_VIDEO_DURATION=3.0` (the loop always produces ≥10 frames); the rebuild can drop it. One ffmpeg-per-frame is another Python-era shape worth revisiting — a single ffmpeg with a select filter can stream all frames in one invocation and cut subprocess overhead ~100×.

### 6. Match algorithm (`_dense_hashes_match`)

**PURPOSE.** Decide whether two dense hashes are the same goal even when the two clips start at different broadcast offsets.

**BEHAVIOR.**
- Both hashes parsed into `{timestamp: hash_int}` maps, `dedup_match.py:132-142`.
- Reject if either side has fewer than `MIN_CONSECUTIVE_MATCHES=3` frames, `dedup_match.py:147-148`, `config.py:79`.
- Outer double-loop over all `(start_a, start_b)` pairs establishes candidate offset `offset = start_b - start_a`, `dedup_match.py:155-157`.
- For each offset, walk `ts_a` in order; expected `ts_b = ts_a + offset`; accept any actual B-timestamp within `tolerance = interval_a / 2 = 0.125 s`, `dedup_match.py:163-171`.
- Per-frame match: Hamming ≤ `MAX_HAMMING_DISTANCE=10` bits, `config.py:78`, `dedup_match.py:174-177`.
- On match, `consecutive++`; on miss, reset to 0, `dedup_match.py:179-185`.
- Return True as soon as `consecutive` reaches 3, `dedup_match.py:180-183`.
- Legacy 3-hash format falls back to "2 of 3 match, any order," `dedup_match.py:82-99`.

**REMARKS.** 3 consecutive matches at 0.25 s = 0.75 s of matching video, chosen to reject false positives from goals scored 60 s apart in the same match with similar celebration framing (comment at `config.py:79`). Offset-tolerance of `interval/2` is what makes the algorithm robust to two clips whose 0.25 s sample grids happen to be shifted by ~0.1 s; without it, the same broadcast frame at slightly-different sample offsets would appear un-matched. Worst-case complexity is O(|A|²·|B|²) via nested linear scans; for 120-frame clips that's ~200M inner iterations. Real clips short-circuit early; the rebuild should still index `frames_b` by rounded timestamp so the tolerance lookup is O(1).

### 7. Ordering — MD5, perceptual, S3 corpus

**PURPOSE.** Run the cheap exact-match check first; only spend perceptual compute where it could actually change the answer.

**BEHAVIOR.**
- MD5 is computed at download time, `download.py:628`, long before any perceptual work.
- Perceptual hash is generated AFTER AI vision validation succeeds; comment at `download.py:559`: "Does NOT generate perceptual hash here"; comment at `hashing.py:153`: "Called AFTER AI validation to avoid wasting compute on non-soccer videos."
- Within a batch: MD5 dedup collapses byte-identical candidates first; perceptual dedup runs on the survivors.
- Against the S3 corpus: MD5 → S3 HEAD via the key convention; if hit, skip perceptual entirely.
- Perceptual is invoked only between MD5-differing pairs.
- Empty perceptual hash on either side → treated as no-signal, pair NOT collapsed, `dedup_match.py:70-71`.

**REMARKS.** The AI-vision-before-perceptual-hash ordering matters for compute budget — vision drops a significant fraction of candidates, so perceptual runs on ~1 in 3–5 downloaded clips. Reverse the order and ffmpeg subprocess count grows 3–5×. The rebuild should preserve this exact ordering.

### 8. Failure modes

**PURPOSE.** Every stage degrades to a typed, retriable outcome — never silent data loss.

**BEHAVIOR.**
- Missing/empty perceptual hash: `_perceptual_hashes_match` short-circuits False, `dedup_match.py:70-71`.
- No frames extracted: activity returns `{"perceptual_hash": "", "error": "no_frames_extracted"}`, `hashing.py:194-197`.
- Per-frame ffmpeg failure: `extract_frame_hash_normalized` returns `""` on non-zero return or timeout, `hashing.py:91-92, 113-117`; that timestamp is skipped, generation continues.
- ffprobe failure: caught, logged, returns zeros; downstream duration=0 fails MIN gate and file is deleted, `download.py:715-720, 585`.
- File < 1 KB: deleted, `RuntimeError` raised for Temporal retry, `download.py:520-525`.
- Invalid hash format on emit: returned as error, upload skipped, `hashing.py:198-202`.
- Heartbeat before every ffmpeg subprocess — the fix for the "4 concurrent hash-gens all timing out" mode; comment at `hashing.py:67-70` marks this CRITICAL.
- Hash parse errors: `_hamming_distance` returns 64 (max) on `ValueError/TypeError`, `dedup_match.py:32-37`; `_dense_hashes_match` returns False on any Exception, `dedup_match.py:189-190`.

**REMARKS.** The "return False on any Exception" blanket at `dedup_match.py:189-190` is intentionally lenient — over-uploading is a lesser evil than crashing the upload workflow — but the rebuild should log the exception rather than swallow it silently.

### 9. Historical fixes visible in comments

**PURPOSE.** Comments preserve production incidents whose fixes aren't obvious from code shape alone.

**BEHAVIOR.**
- Paderborn-Wolfsburg post-mortem, 2026-05-25: an upstream tweet-URL bug produced 13/14/17-digit Snowflakes that only failed at the syndication 404 stage; fix at `download.py:245-273` adds `MIN_SNOWFLAKE_LEN=18` and raises `VideoMalformedURLError` with `failure_mode=truncated_snowflake` so Grafana sees the shape.
- Phase 3 (P3a, 2026-05-26) extracted vision + hashing into their own modules from `src/activities/download.py`; back-compat re-exports at `download.py:758-772` preserve the old import path.
- Phase 3 (P3b, 2026-05-26) split dedup helpers into `src/utils/dedup_match.py`, header at `dedup_match.py:1-14`.
- `HASH_VERSION="dense:0.25"` (`config.py:77`) is stored per-video in Mongo precisely so an algorithm swap (Phase 5 image-embeddings anticipated at `hashing.py:8-11`) can coexist with old hashes rather than force a corpus-wide re-hash.
- Aspect band (1.75-1.82) is a distribution-driven decision, computed against prod S3 on 2026-06-30, `config.py:60-66`.
- `MODULE = "download"` kept inside `hashing.py` (`hashing.py:15-16, 30-31`) so existing Grafana dashboards filtering `module="download"` keep working after the split — filename is documentation-of-organization, MODULE is documentation-of-identity.

**REMARKS.** Every comment-cited incident produced the pair (typed error, queryable Grafana field) — the Go rebuild should carry that discipline forward. Legacy hash formats (`hash1:hash2:hash3` and `duration:hash1:hash2:hash3`) still live in `_parse_perceptual_hash` at `dedup_match.py:220-230`; the rebuild can drop them if the migration plan re-hashes surviving legacy S3 objects, else keep the branch.

---
