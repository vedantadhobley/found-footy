// Package video owns the video_assets (canonical byte-store) +
// video_shares (public share IDs, s_<12-hex>) two-table split.
// Enforces URL stability + rank uniqueness invariants — see §4 video
// domain "Load-bearing invariant" paragraph.
package video
