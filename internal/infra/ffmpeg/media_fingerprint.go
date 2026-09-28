// media_fingerprint.go — versioned identity for the encoded media in a file.
// It excludes container tags and mux order, but includes every audio/video
// stream's playback metadata, packet payloads, and presentation timing.
package ffmpeg

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"strconv"
	"strings"
	"time"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// MediaFingerprintVersion keeps the original canonical encoding fixed.
// Fingerprints from different versions must never be compared as exact media.
const MediaFingerprintVersion = dvideo.MediaFingerprintVersion

// MediaFingerprintUnknownDurationVersion has an explicit unknown-duration
// marker and a stream end boundary. The original version never changes.
const MediaFingerprintUnknownDurationVersion = dvideo.MediaFingerprintUnknownDurationVersion

type fingerprintStream struct {
	Index             int             `json:"index"`
	CodecType         string          `json:"codec_type"`
	CodecName         string          `json:"codec_name"`
	Profile           string          `json:"profile"`
	Width             int             `json:"width"`
	Height            int             `json:"height"`
	PixelFormat       string          `json:"pix_fmt"`
	SampleAspectRatio string          `json:"sample_aspect_ratio"`
	ColorRange        string          `json:"color_range"`
	ColorSpace        string          `json:"color_space"`
	ColorTransfer     string          `json:"color_transfer"`
	ColorPrimaries    string          `json:"color_primaries"`
	ChromaLocation    string          `json:"chroma_location"`
	SampleRate        string          `json:"sample_rate"`
	Channels          int             `json:"channels"`
	ChannelLayout     string          `json:"channel_layout"`
	ExtradataHash     string          `json:"extradata_hash"`
	Disposition       json.RawMessage `json:"disposition"`
	SideData          json.RawMessage `json:"side_data_list"`
	Tags              struct {
		Language string `json:"language"`
	} `json:"tags"`
}

// fingerprintProbeStream separates additional timing evidence from the v1
// metadata encoding, so existing fingerprints remain byte-for-byte stable.
type fingerprintProbeStream struct {
	fingerprintStream
	TimeBase   string `json:"time_base"`
	StartPTS   *int64 `json:"start_pts"`
	DurationTS *int64 `json:"duration_ts"`
}

type fingerprintPacket struct {
	StreamIndex int             `json:"stream_index"`
	PTSRaw      *int64          `json:"pts"`
	DTSRaw      *int64          `json:"dts"`
	DurationRaw *int64          `json:"duration"`
	PTS         string          `json:"pts_time"`
	DTS         string          `json:"dts_time"`
	Duration    string          `json:"duration_time"`
	Size        string          `json:"size"`
	DataHash    string          `json:"data_hash"`
	SideData    json.RawMessage `json:"side_data_list"`
}

type fingerprintPacketState struct {
	legacy          hash.Hash
	unknownDuration hash.Hash
	count           int
	missingDuration bool
	missingRawTime  bool
	missingRawSpan  bool
}

// MediaFingerprint hashes ffprobe evidence without reading packet payloads
// into Go memory. An unknown packet duration is safe only with an exact
// stream end boundary; other incomplete evidence falls back to raw MD5.
// The result is scoped by the caller to an event.
func (c *Client) MediaFingerprint(ctx context.Context, videoPath string) (_ string, resultErr error) {
	if err := statInput(videoPath); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, c.denseTimeout)
	defer cancel()
	if err := c.acquire(ctx); err != nil {
		return "", err
	}
	defer c.release()

	started := time.Now()
	defer func() {
		c.observe(ctx, vocabulary.ActionFFmpegProbe, "media_fingerprint", started, resultErr)
	}()
	streamArgs := []string{"-v", "quiet", "-print_format", "json", "-show_data_hash", "sha256", "-show_streams",
		"-show_entries", "stream=index,codec_type,codec_name,profile,width,height,pix_fmt,sample_aspect_ratio,color_range,color_space,color_transfer,color_primaries,chroma_location,sample_rate,channels,channel_layout,extradata_hash,time_base,start_pts,duration_ts:stream_side_data=side_data_type,rotation,displaymatrix", videoPath}
	stdout, stderr, err := c.run(ctx, c.ffprobePath, streamArgs)
	if err != nil {
		return "", classify(ctx, true, err, stderr)
	}
	var probe struct {
		Streams []fingerprintProbeStream `json:"streams"`
	}
	if err := json.Unmarshal(stdout, &probe); err != nil {
		return "", fmt.Errorf("%w: decode fingerprint streams: %v", ErrProbeFailed, err)
	}
	if len(probe.Streams) == 0 {
		return "", fmt.Errorf("%w: fingerprint has no streams", ErrProbeFailed)
	}

	packetStates := make(map[int]*fingerprintPacketState, len(probe.Streams))
	seenVideo := false
	for _, stream := range probe.Streams {
		if stream.CodecType != "video" && stream.CodecType != "audio" {
			return "", fmt.Errorf("%w: unsupported %s stream", ErrProbeFailed, stream.CodecType)
		}
		if stream.CodecType == "video" {
			seenVideo = true
		}
		if stream.CodecName == "" || stream.ExtradataHash == "" {
			return "", fmt.Errorf("%w: stream %d lacks codec evidence", ErrProbeFailed, stream.Index)
		}
		if _, exists := packetStates[stream.Index]; exists {
			return "", fmt.Errorf("%w: duplicate stream index %d", ErrProbeFailed, stream.Index)
		}
		packetStates[stream.Index] = &fingerprintPacketState{
			legacy: sha256.New(), unknownDuration: sha256.New(),
		}
	}
	if !seenVideo {
		return "", fmt.Errorf("%w: fingerprint has no video stream", ErrProbeFailed)
	}

	packetArgs := []string{"-v", "quiet", "-print_format", "json", "-show_data_hash", "sha256", "-show_packets",
		"-show_entries", "packet=stream_index,pts,dts,duration,pts_time,dts_time,duration_time,size,data_hash:packet_side_data", videoPath}
	stderr, err = c.runStream(ctx, c.ffprobePath, packetArgs, func(stdout io.Reader) error {
		return fingerprintPackets(stdout, packetStates)
	})
	if err != nil {
		return "", classify(ctx, true, err, stderr)
	}
	unknownDuration := false
	for _, stream := range probe.Streams {
		state := packetStates[stream.Index]
		if state.count == 0 {
			return "", fmt.Errorf("%w: stream %d has no packets", ErrProbeFailed, stream.Index)
		}
		unknownDuration = unknownDuration || state.missingDuration
	}

	version := MediaFingerprintVersion
	if unknownDuration {
		version = MediaFingerprintUnknownDurationVersion
		for _, stream := range probe.Streams {
			state := packetStates[stream.Index]
			if state.missingRawTime || state.missingRawSpan || stream.StartPTS == nil || stream.DurationTS == nil ||
				*stream.DurationTS <= 0 || !validFingerprintTimeBase(stream.TimeBase) {
				return "", fmt.Errorf("%w: stream %d lacks exact end-boundary evidence", ErrProbeFailed, stream.Index)
			}
		}
	}

	root := sha256.New()
	writeFingerprintField(root, version)
	writeFingerprintField(root, strconv.Itoa(len(probe.Streams)))
	for _, stream := range probe.Streams {
		// Keep the v1 metadata encoding exactly as shipped. v2 adds the
		// stream's exact time base, start, and end to distinguish final-frame
		// holds when ffprobe cannot report individual packet durations.
		meta, err := json.Marshal(stream.fingerprintStream)
		if err != nil {
			return "", fmt.Errorf("%w: encode stream: %v", ErrProbeFailed, err)
		}
		writeFingerprintField(root, string(meta))
		if unknownDuration {
			writeFingerprintField(root, stream.TimeBase)
			writeFingerprintField(root, strconv.FormatInt(*stream.StartPTS, 10))
			writeFingerprintField(root, strconv.FormatInt(*stream.DurationTS, 10))
		}
	}
	// v1 writes every stream's metadata before any packet summary. Preserve
	// that ordering; interleaving summaries would silently change old hashes.
	for _, stream := range probe.Streams {
		state := packetStates[stream.Index]
		writeFingerprintField(root, strconv.Itoa(state.count))
		digest := state.legacy
		if unknownDuration {
			digest = state.unknownDuration
		}
		writeFingerprintField(root, hex.EncodeToString(digest.Sum(nil)))
	}
	return version + ":" + hex.EncodeToString(root.Sum(nil)), nil
}

// fingerprintPackets groups packets by stream, so a harmless audio/video
// interleave change does not change identity. Order inside each stream does.
func fingerprintPackets(r io.Reader, streams map[int]*fingerprintPacketState) error {
	dec := json.NewDecoder(r)
	first, err := dec.Token()
	if err != nil || first != json.Delim('{') {
		return fmt.Errorf("%w: malformed packet probe", ErrProbeFailed)
	}
	found := false
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return fmt.Errorf("%w: packet probe key: %v", ErrProbeFailed, err)
		}
		if key != "packets" {
			var ignored json.RawMessage
			if err := dec.Decode(&ignored); err != nil {
				return fmt.Errorf("%w: packet probe field: %v", ErrProbeFailed, err)
			}
			continue
		}
		found = true
		start, err := dec.Token()
		if err != nil || start != json.Delim('[') {
			return fmt.Errorf("%w: malformed packet list", ErrProbeFailed)
		}
		for dec.More() {
			var packet fingerprintPacket
			if err := dec.Decode(&packet); err != nil {
				return fmt.Errorf("%w: decode packet: %v", ErrProbeFailed, err)
			}
			state, ok := streams[packet.StreamIndex]
			if !ok || packet.PTS == "" || packet.PTS == "N/A" || packet.DTS == "" || packet.DTS == "N/A" ||
				packet.Size == "" ||
				!strings.HasPrefix(packet.DataHash, "SHA256:") || len(packet.DataHash) != len("SHA256:")+64 {
				return fmt.Errorf("%w: incomplete packet evidence", ErrProbeFailed)
			}
			if packet.Duration == "N/A" {
				packet.Duration = ""
			}
			if packet.Duration == "" {
				state.missingDuration = true
			}
			if packet.PTSRaw == nil || packet.DTSRaw == nil {
				state.missingRawTime = true
			}
			if packet.Duration != "" && packet.DurationRaw == nil {
				state.missingRawSpan = true
			}
			for _, field := range []string{packet.PTS, packet.DTS, packet.Duration, packet.Size, packet.DataHash, string(packet.SideData)} {
				writeFingerprintField(state.legacy, field)
			}
			if packet.PTSRaw != nil && packet.DTSRaw != nil {
				writeFingerprintField(state.unknownDuration, strconv.FormatInt(*packet.PTSRaw, 10))
				writeFingerprintField(state.unknownDuration, strconv.FormatInt(*packet.DTSRaw, 10))
			}
			if packet.Duration == "" {
				writeFingerprintField(state.unknownDuration, "unknown")
			} else {
				writeFingerprintField(state.unknownDuration, "known")
				if packet.DurationRaw != nil {
					writeFingerprintField(state.unknownDuration, strconv.FormatInt(*packet.DurationRaw, 10))
				}
			}
			for _, field := range []string{packet.Size, packet.DataHash, string(packet.SideData)} {
				writeFingerprintField(state.unknownDuration, field)
			}
			state.count++
		}
		if end, err := dec.Token(); err != nil || end != json.Delim(']') {
			return fmt.Errorf("%w: packet list not closed", ErrProbeFailed)
		}
	}
	if end, err := dec.Token(); err != nil || end != json.Delim('}') || !found {
		return fmt.Errorf("%w: packet probe not closed", ErrProbeFailed)
	}
	return nil
}

// validFingerprintTimeBase rejects missing or malformed stream timing before
// an unknown packet duration can claim exact media identity.
func validFingerprintTimeBase(value string) bool {
	numerator, denominator, ok := strings.Cut(value, "/")
	if !ok {
		return false
	}
	n, nErr := strconv.ParseInt(numerator, 10, 64)
	d, dErr := strconv.ParseInt(denominator, 10, 64)
	return nErr == nil && dErr == nil && n > 0 && d > 0
}

func writeFingerprintField(h hash.Hash, field string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(field)))
	_, _ = h.Write(length[:])
	_, _ = io.WriteString(h, field)
}
