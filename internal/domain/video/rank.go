// rank.go — share ranking rules. Encoded as a comparison function so
// repo code can sort in memory before rewriting ranks atomically.
package video

// CompareShares returns:
//   -1 if a ranks BETTER than b (should come first)
//    0 if they're equivalent for ranking purposes
//   +1 if a ranks WORSE than b
//
// The rebuild-plan §3 ranking rule: verified > unverified,
// higher popularity > lower, larger file_size > smaller. Ties broken
// by CreatedAt (older = better; established shares stay stable).
//
// Callers use it via sort.Slice / sort.SliceStable and then rewrite
// ranks 1..N atomically. The (event_id, rank) UNIQUE partial index
// enforces the invariant at write time — the 2026-06-30 rank-drift
// bug (ranks 0,0,2,3 on Norway-CIV) becomes impossible.
//
// The Asset lookup for popularity + file_size is passed in so this
// function stays pure — the repo knows how to fetch the asset row for
// each share.
func CompareShares(a, b *Share, aAsset, bAsset *Asset) int {
	// 1. timestamp_verified beats unverified
	if a.TimestampVerified != b.TimestampVerified {
		if a.TimestampVerified {
			return -1
		}
		return +1
	}
	// 2. popularity — higher wins
	if aAsset.Popularity != bAsset.Popularity {
		if aAsset.Popularity > bAsset.Popularity {
			return -1
		}
		return +1
	}
	// 3. file_size — larger wins (proxy for resolution / quality)
	if aAsset.FileSizeBytes != bAsset.FileSizeBytes {
		if aAsset.FileSizeBytes > bAsset.FileSizeBytes {
			return -1
		}
		return +1
	}
	// 4. tiebreaker: older CreatedAt wins — established shares stay stable
	if a.CreatedAt.Before(b.CreatedAt) {
		return -1
	}
	if a.CreatedAt.After(b.CreatedAt) {
		return +1
	}
	return 0
}
