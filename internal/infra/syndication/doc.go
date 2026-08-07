// Package syndication is the Twitter syndication-API client used to
// download the actual video bytes from a tweet. Includes the snowflake-
// ID validation gate (the fix for the audit §8 truncated-ID bug).
//
// NOTE: the `-movflags +faststart` normalization that decisions.md 2026-07-02
// marked a hard requirement is NOT applied on this path — `ffmpeg.Faststart`
// has no callers and staging/assets hold the raw downloaded bytes. Unmet
// requirement, tracked in #179 (audit-2026-08-05).
package syndication
