// read.go — read-model projections for the API read path (#167). These are
// NOT storage rows; they're the exact shapes the /events and /videos endpoints
// need, so the pg repo can return one JOIN/CTE result instead of the handler
// stitching Asset + Share together. Kept in the video domain package because
// they're video-domain shapes, but they're consumed only by the read API.
package video

// LiveClip is one displayable clip: an active share joined to its LIVE asset
// (superseded_by IS NULL), with rank derived from the current verified,
// popularity, size, age, and share-ID evidence (rank 1 = primary).
// Backs the `videos` array on the fixture/event reads. Superseded/removed
// clips never appear here — their old s_<hex> URLs still resolve via the
// redirect handler (see ResolvedShare).
type LiveClip struct {
	ShareID         string
	Rank            int
	Verified        bool
	ExtractedMinute *int
	Popularity      int
	Width, Height   int
	DurationMS      int
}

// ObjectRef is a Garage object location (bucket + key). Used by the destroy /
// retention-reclaim paths to delete an event's asset bytes (#172/#176).
type ObjectRef struct {
	Bucket string
	Key    string
}

// ResolvedShare is what the GET /videos/{share_id} redirect handler needs:
// the share's State (drives 302 vs 410) and the LIVE asset's S3 location after
// following the superseded_by chain (the 302 target). Resolution returns
// ErrNotFound when the share id was never minted (→ 404). A superseded share
// still resolves — that's the load-bearing URL-stability invariant.
type ResolvedShare struct {
	State  ShareState
	Bucket string
	Key    string
}
