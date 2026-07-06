// Package syndication is the Twitter syndication-API client used to
// download the actual video bytes from a tweet. Includes the snowflake-
// ID validation gate (the fix for the audit §8 truncated-ID bug).
// Video-download normalization runs -movflags +faststart per
// decisions.md 2026-07-02.
package syndication
