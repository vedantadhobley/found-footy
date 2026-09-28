// media_identity.go — canonical version label for exact encoded-media identity.
package video

// MediaFingerprintVersion is the original on-disk contract for streams whose
// packets all report duration. Keep it stable for accepted in-flight assets.
const MediaFingerprintVersion = "media-v1"

// MediaFingerprintUnknownDurationVersion also covers packets with unknown
// duration, provided their stream has a complete playback end boundary.
const MediaFingerprintUnknownDurationVersion = "media-v2"
