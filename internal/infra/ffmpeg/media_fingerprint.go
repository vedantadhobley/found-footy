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

// MediaFingerprintVersion changes whenever the canonical fields or encoding
// change. Fingerprints from different versions must never be compared.
const MediaFingerprintVersion = dvideo.MediaFingerprintVersion

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

type fingerprintPacket struct {
	StreamIndex int             `json:"stream_index"`
	PTS         string          `json:"pts_time"`
	DTS         string          `json:"dts_time"`
	Duration    string          `json:"duration_time"`
	Size        string          `json:"size"`
	DataHash    string          `json:"data_hash"`
	SideData    json.RawMessage `json:"side_data_list"`
}

// MediaFingerprint hashes decoded ffprobe evidence without reading packet
// payloads into Go memory. A missing field fails closed: callers may keep the
// raw-MD5 path, but must not infer two files are identical from partial data.
// The result is scoped by the caller to an event; this method has no event or
// storage access.
func (c *Client) MediaFingerprint(ctx context.Context, videoPath string) (string, error) {
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
	streamArgs := []string{"-v", "quiet", "-print_format", "json", "-show_data_hash", "sha256", "-show_streams",
		"-show_entries", "stream=index,codec_type,codec_name,profile,width,height,pix_fmt,sample_aspect_ratio,color_range,color_space,color_transfer,color_primaries,chroma_location,sample_rate,channels,channel_layout,extradata_hash:stream_side_data=side_data_type,rotation,displaymatrix", videoPath}
	stdout, stderr, err := c.run(ctx, c.ffprobePath, streamArgs)
	if err != nil {
		c.observe(ctx, vocabulary.ActionFFmpegProbe, "media_fingerprint", started, err)
		return "", classify(ctx, true, err, stderr)
	}
	var probe struct {
		Streams []fingerprintStream `json:"streams"`
	}
	if err := json.Unmarshal(stdout, &probe); err != nil {
		return "", fmt.Errorf("%w: decode fingerprint streams: %v", ErrProbeFailed, err)
	}
	if len(probe.Streams) == 0 {
		return "", fmt.Errorf("%w: fingerprint has no streams", ErrProbeFailed)
	}

	root := sha256.New()
	writeFingerprintField(root, MediaFingerprintVersion)
	writeFingerprintField(root, strconv.Itoa(len(probe.Streams)))
	packetDigests := make(map[int]hash.Hash, len(probe.Streams))
	packetCounts := make(map[int]int, len(probe.Streams))
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
		if _, exists := packetDigests[stream.Index]; exists {
			return "", fmt.Errorf("%w: duplicate stream index %d", ErrProbeFailed, stream.Index)
		}
		// JSON serialization of this fixed struct is the canonical metadata
		// encoding. ffprobe tags and container/format metadata are excluded.
		meta, err := json.Marshal(stream)
		if err != nil {
			return "", fmt.Errorf("%w: encode stream: %v", ErrProbeFailed, err)
		}
		writeFingerprintField(root, string(meta))
		packetDigests[stream.Index] = sha256.New()
	}
	if !seenVideo {
		return "", fmt.Errorf("%w: fingerprint has no video stream", ErrProbeFailed)
	}

	packetArgs := []string{"-v", "quiet", "-print_format", "json", "-show_data_hash", "sha256", "-show_packets",
		"-show_entries", "packet=stream_index,pts_time,dts_time,duration_time,size,data_hash:packet_side_data", videoPath}
	stderr, err = c.runStream(ctx, c.ffprobePath, packetArgs, func(stdout io.Reader) error {
		return fingerprintPackets(stdout, packetDigests, packetCounts)
	})
	c.observe(ctx, vocabulary.ActionFFmpegProbe, "media_fingerprint", started, err)
	if err != nil {
		return "", classify(ctx, true, err, stderr)
	}
	for _, stream := range probe.Streams {
		if packetCounts[stream.Index] == 0 {
			return "", fmt.Errorf("%w: stream %d has no packets", ErrProbeFailed, stream.Index)
		}
		writeFingerprintField(root, strconv.Itoa(packetCounts[stream.Index]))
		writeFingerprintField(root, hex.EncodeToString(packetDigests[stream.Index].Sum(nil)))
	}
	return MediaFingerprintVersion + ":" + hex.EncodeToString(root.Sum(nil)), nil
}

// fingerprintPackets groups packets by stream, so a harmless audio/video
// interleave change does not change identity. Order inside each stream does.
func fingerprintPackets(r io.Reader, streams map[int]hash.Hash, counts map[int]int) error {
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
			h, ok := streams[packet.StreamIndex]
			if !ok || packet.PTS == "" || packet.PTS == "N/A" || packet.DTS == "" || packet.DTS == "N/A" ||
				packet.Duration == "" || packet.Duration == "N/A" || packet.Size == "" ||
				!strings.HasPrefix(packet.DataHash, "SHA256:") || len(packet.DataHash) != len("SHA256:")+64 {
				return fmt.Errorf("%w: incomplete packet evidence", ErrProbeFailed)
			}
			for _, field := range []string{packet.PTS, packet.DTS, packet.Duration, packet.Size, packet.DataHash, string(packet.SideData)} {
				writeFingerprintField(h, field)
			}
			counts[packet.StreamIndex]++
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

func writeFingerprintField(h hash.Hash, field string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(field)))
	_, _ = h.Write(length[:])
	_, _ = io.WriteString(h, field)
}
