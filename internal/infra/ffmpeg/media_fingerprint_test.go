// media_fingerprint_test.go — fake-backed media-identity invariants and opt-in real-clip regressions.
package ffmpeg

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vedantadhobley/found-footy/internal/config"
)

const fingerprintStreams = `{"streams":[
	{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,
	 "extradata_hash":"SHA256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	 "tags":{"title":"different upload title","creation_time":"tomorrow"}},
	{"index":1,"codec_type":"audio","codec_name":"aac","sample_rate":"48000","channels":2,
	 "extradata_hash":"SHA256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
]}`

const fingerprintPacketsA = `{"packets":[
	{"stream_index":0,"pts_time":"0.000000","dts_time":"0.000000","duration_time":"0.040000","size":"4","data_hash":"SHA256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	{"stream_index":1,"pts_time":"0.000000","dts_time":"0.000000","duration_time":"0.020000","size":"2","data_hash":"SHA256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	{"stream_index":0,"pts_time":"0.040000","dts_time":"0.040000","duration_time":"0.040000","size":"4","data_hash":"SHA256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}
]}`

// ffprobe omits duration_time when AVPacket.duration is unknown. Real Twitter
// MP4s can have this shape for every video packet while retaining a complete
// per-stream end boundary and exact packet PTS/DTS values.
const fingerprintUnknownDurationStreams = `{"streams":[
	{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,
	 "extradata_hash":"SHA256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	 "time_base":"1/600000","start_pts":0,"duration_ts":1200000,
	 "tags":{"title":"first upload"}},
	{"index":1,"codec_type":"audio","codec_name":"aac","sample_rate":"48000","channels":2,
	 "extradata_hash":"SHA256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	 "time_base":"1/48000","start_pts":0,"duration_ts":96000}
]}`

const fingerprintUnknownDurationPackets = `{"packets":[
	{"stream_index":0,"pts":0,"dts":0,"pts_time":"0.000000","dts_time":"0.000000","size":"4","data_hash":"SHA256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	{"stream_index":1,"pts":0,"dts":0,"duration":960,"pts_time":"0.000000","dts_time":"0.000000","duration_time":"0.020000","size":"2","data_hash":"SHA256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	{"stream_index":0,"pts":20000,"dts":20000,"pts_time":"0.033333","dts_time":"0.033333","size":"4","data_hash":"SHA256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}
]}`

func fingerprintFromFake(t *testing.T, streams, packets string) (string, error) {
	t.Helper()
	c := mustClient(t, config.FFmpegConfig{})
	c.run = func(context.Context, string, []string) ([]byte, []byte, error) {
		return []byte(streams), nil, nil
	}
	c.runStream = func(_ context.Context, _ string, _ []string, consume func(io.Reader) error) ([]byte, error) {
		return nil, consume(strings.NewReader(packets))
	}
	return c.MediaFingerprint(context.Background(), tempFile(t))
}

func TestMediaFingerprintIgnoresContainerTagsAndInterleave(t *testing.T) {
	base, err := fingerprintFromFake(t, fingerprintStreams, fingerprintPacketsA)
	if err != nil {
		t.Fatal(err)
	}
	interleaved := strings.Replace(fingerprintPacketsA,
		`{"stream_index":1,"pts_time":"0.000000","dts_time":"0.000000","duration_time":"0.020000","size":"2","data_hash":"SHA256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	{"stream_index":0,"pts_time":"0.040000"`,
		`{"stream_index":0,"pts_time":"0.040000"`, 1)
	interleaved = strings.Replace(interleaved, `"data_hash":"SHA256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}`,
		`"data_hash":"SHA256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
	{"stream_index":1,"pts_time":"0.000000","dts_time":"0.000000","duration_time":"0.020000","size":"2","data_hash":"SHA256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`, 1)
	if !strings.HasPrefix(base, MediaFingerprintVersion+":") {
		t.Fatalf("fingerprint %q lacks version prefix", base)
	}
	for name, tc := range map[string]struct{ streams, packets string }{
		"upload title":      {strings.Replace(fingerprintStreams, "different upload title", "new metadata only", 1), fingerprintPacketsA},
		"creation date":     {strings.Replace(fingerprintStreams, "tomorrow", "yesterday", 1), fingerprintPacketsA},
		"encoder tag":       {strings.Replace(fingerprintStreams, `"creation_time":"tomorrow"`, `"creation_time":"tomorrow","encoder":"different muxer"`, 1), fingerprintPacketsA},
		"format tag":        {strings.TrimSuffix(fingerprintStreams, "}") + `,"format":{"tags":{"creation_time":"earlier"}}}`, fingerprintPacketsA},
		"packet interleave": {fingerprintStreams, interleaved},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := fingerprintFromFake(t, tc.streams, tc.packets)
			if err != nil {
				t.Fatal(err)
			}
			if got != base {
				t.Fatalf("metadata/interleave changed identity: %q != %q", got, base)
			}
		})
	}
}

func TestMediaFingerprintOriginalVersionIsStableWithExtraProbeFields(t *testing.T) {
	base, err := fingerprintFromFake(t, fingerprintStreams, fingerprintPacketsA)
	if err != nil {
		t.Fatal(err)
	}
	const originalDigest = "media-v1:05e358ae85e6a3c2eab7f385ec9b14f5c8d530b21415062a1cf75b768e1f21c8"
	if base != originalDigest {
		t.Fatalf("media-v1 digest changed: %q != %q", base, originalDigest)
	}
	withTiming, err := fingerprintFromFake(t,
		strings.Replace(fingerprintStreams, `"codec_name":"h264"`, `"codec_name":"h264","time_base":"1/1000","start_pts":0,"duration_ts":80`, 1),
		strings.Replace(fingerprintPacketsA, `"pts_time":"0.000000"`, `"pts":0,"dts":0,"pts_time":"0.000000"`, 1))
	if err != nil {
		t.Fatal(err)
	}
	if withTiming != base {
		t.Fatalf("additional probe fields changed media-v1: %q != %q", withTiming, base)
	}
}

func TestMediaFingerprintUnknownPacketDurationUsesStreamEnd(t *testing.T) {
	base, err := fingerprintFromFake(t, fingerprintUnknownDurationStreams, fingerprintUnknownDurationPackets)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(base, MediaFingerprintUnknownDurationVersion+":") {
		t.Fatalf("unknown-duration fingerprint = %q, want media-v2", base)
	}
	metadataOnly, err := fingerprintFromFake(t,
		strings.Replace(fingerprintUnknownDurationStreams, "first upload", "new container title", 1),
		fingerprintUnknownDurationPackets)
	if err != nil || metadataOnly != base {
		t.Fatalf("metadata-only copy split: %q, %v", metadataOnly, err)
	}
	unknownSpelledOut, err := fingerprintFromFake(t, fingerprintUnknownDurationStreams,
		strings.ReplaceAll(fingerprintUnknownDurationPackets, `"size":"4"`, `"duration_time":"N/A","size":"4"`))
	if err != nil || unknownSpelledOut != base {
		t.Fatalf("N/A and absent duration should have one identity: %q, %v", unknownSpelledOut, err)
	}
	for name, tc := range map[string]struct{ streams, packets string }{
		"different final-frame hold": {
			strings.Replace(fingerprintUnknownDurationStreams, `"duration_ts":1200000`, `"duration_ts":1800000`, 1),
			fingerprintUnknownDurationPackets,
		},
		"different raw presentation time": {
			fingerprintUnknownDurationStreams,
			strings.Replace(fingerprintUnknownDurationPackets, `"pts":20000`, `"pts":20001`, 1),
		},
		"different raw audio duration": {
			fingerprintUnknownDurationStreams,
			strings.Replace(fingerprintUnknownDurationPackets, `"duration":960`, `"duration":961`, 1),
		},
		"different encoded packet": {
			fingerprintUnknownDurationStreams,
			strings.Replace(fingerprintUnknownDurationPackets, `"data_hash":"SHA256:cccccccc`, `"data_hash":"SHA256:dddddddd`, 1),
		},
		"different time base": {
			strings.Replace(fingerprintUnknownDurationStreams, `"time_base":"1/600000"`, `"time_base":"1/600001"`, 1),
			fingerprintUnknownDurationPackets,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := fingerprintFromFake(t, tc.streams, tc.packets)
			if err != nil {
				t.Fatal(err)
			}
			if got == base {
				t.Fatal("playback change collapsed into the same identity")
			}
		})
	}
}

func TestMediaFingerprintUnknownDurationFailsWithoutEndEvidence(t *testing.T) {
	for name, tc := range map[string]struct{ streams, packets string }{
		"missing stream duration": {
			strings.Replace(fingerprintUnknownDurationStreams, `,"duration_ts":1200000`, "", 1),
			fingerprintUnknownDurationPackets,
		},
		"missing stream start": {
			strings.Replace(fingerprintUnknownDurationStreams, `"start_pts":0,`, "", 1),
			fingerprintUnknownDurationPackets,
		},
		"zero stream duration": {
			strings.Replace(fingerprintUnknownDurationStreams, `"duration_ts":1200000`, `"duration_ts":0`, 1),
			fingerprintUnknownDurationPackets,
		},
		"missing time base": {
			strings.Replace(fingerprintUnknownDurationStreams, `"time_base":"1/600000",`, "", 1),
			fingerprintUnknownDurationPackets,
		},
		"missing raw packet timestamp": {
			fingerprintUnknownDurationStreams,
			strings.Replace(fingerprintUnknownDurationPackets, `"pts":20000,`, "", 1),
		},
		"missing known raw packet duration": {
			fingerprintUnknownDurationStreams,
			strings.Replace(fingerprintUnknownDurationPackets, `"duration":960,`, "", 1),
		},
		"missing packet presentation timestamp": {
			fingerprintUnknownDurationStreams,
			strings.Replace(fingerprintUnknownDurationPackets, `"pts_time":"0.033333",`, "", 1),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := fingerprintFromFake(t, tc.streams, tc.packets); err == nil {
				t.Fatalf("incomplete evidence produced %q", got)
			}
		})
	}
}

func TestMediaFingerprintRetainsPlaybackDifferences(t *testing.T) {
	base, err := fingerprintFromFake(t, fingerprintStreams, fingerprintPacketsA)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct{ streams, packets string }{
		"video packet": {fingerprintStreams, strings.Replace(fingerprintPacketsA, "cccccccc", "dddddddd", 1)},
		"audio packet": {fingerprintStreams, strings.Replace(fingerprintPacketsA, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\"}", "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd\"}", 1)},
		"timing":       {fingerprintStreams, strings.Replace(fingerprintPacketsA, `"pts_time":"0.040000"`, `"pts_time":"1.040000"`, 1)},
		"rotation":     {strings.Replace(fingerprintStreams, `"tags":{"title"`, `"side_data_list":[{"side_data_type":"Display Matrix","rotation":90}],"tags":{"title"`, 1), fingerprintPacketsA},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := fingerprintFromFake(t, tc.streams, tc.packets)
			if err != nil {
				t.Fatal(err)
			}
			if got == base {
				t.Fatal("playback difference collapsed")
			}
		})
	}
}

func TestMediaFingerprintRejectsIncompleteEvidence(t *testing.T) {
	for name, packets := range map[string]string{
		"missing timestamp": strings.Replace(fingerprintPacketsA, `"pts_time":"0.040000",`, "", 1),
		"missing audio":     strings.Replace(fingerprintPacketsA, `{"stream_index":1,"pts_time":"0.000000","dts_time":"0.000000","duration_time":"0.020000","size":"2","data_hash":"SHA256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},`, "", 1),
		"truncated":         fingerprintPacketsA[:len(fingerprintPacketsA)-2],
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := fingerprintFromFake(t, fingerprintStreams, packets); err == nil {
				t.Fatal("incomplete evidence must not produce an identity")
			}
		})
	}
}

// TestMediaFingerprintLocalCorpus is an opt-in real-ffprobe characterization
// harness. Media stays outside the repository; CI runs only the fake-backed
// invariants above.
func TestMediaFingerprintLocalCorpus(t *testing.T) {
	dir := os.Getenv("FF_MEDIA_FINGERPRINT_CORPUS")
	if dir == "" {
		t.Skip("set FF_MEDIA_FINGERPRINT_CORPUS to local MP4 copies")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.mp4"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("corpus glob: %v (%d files)", err, len(paths))
	}
	c := mustClient(t, config.FFmpegConfig{FFprobePath: os.Getenv("FF_MEDIA_FFPROBE")})
	for _, path := range paths {
		fingerprint, err := c.MediaFingerprint(context.Background(), path)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
			continue
		}
		t.Logf("%s %s", filepath.Base(path), fingerprint)
	}
}

// TestMediaFingerprintHistoricalGroups asserts the production-derived media
// identity cases without committing copyrighted clips. Set the directory list
// to the local, read-only MP4 corpus; missing members fail rather than silently
// shrinking the regression. The normal unit suite covers the same rules with
// fake ffprobe output and needs no external binary.
func TestMediaFingerprintHistoricalGroups(t *testing.T) {
	dirs := filepath.SplitList(os.Getenv("FF_MEDIA_FINGERPRINT_HISTORICAL_DIRS"))
	if len(dirs) == 0 || dirs[0] == "" {
		t.Skip("set FF_MEDIA_FINGERPRINT_HISTORICAL_DIRS to the local MP4 corpus directories")
	}
	groups := map[string][]string{
		"raphinha_22": {
			"0229a3ad-68c3-5e39-96f8-96fbdb23ede9",
			"07814816-b574-527c-8431-8ece98f25851",
			"0b18843c-a844-5949-88d6-920d897bcd8d",
			"15e58928-4a5d-5d28-a7ac-f0d4ddf91226",
			"925f4142-ab7e-5600-bf26-b0359357e8b5",
			"b491bc31-23b0-57fa-a256-e2597ca4f99e",
		},
		"raphinha_52": {
			"73037433-6120-5388-84a1-f14abb3e083f",
			"77115242-c6e7-5022-8839-33a097abadbc",
			"96f350e7-ba60-5fde-ba3d-4272961611f6",
			"a7bd7843-0b3a-560f-b61b-f13ca6879492",
		},
		"cancelo_8_long": {
			"20050567-e980-5230-840d-08973e0834f9",
			"9204a3c3-de56-5050-b9fc-18eab761a9da",
			"98bd868d-52ef-513d-8fb4-1edbd9674937",
			"de940c5d-c2d3-58f8-bb45-a9e2cf5998b2",
		},
		"cancelo_8_short": {
			"3b66373a-3a74-5559-9087-4d38192a282d",
			"6330a03b-5459-5e6a-802a-39112d9f8037",
		},
	}
	c := mustClient(t, config.FFmpegConfig{FFprobePath: os.Getenv("FF_MEDIA_FFPROBE")})
	fingerprints := make(map[string]string, len(groups))
	for group, ids := range groups {
		t.Run(group, func(t *testing.T) {
			var representative string
			byteHashes := make(map[[32]byte]string, len(ids))
			for _, id := range ids {
				path := historicalMediaPath(t, dirs, id)
				fingerprint, err := c.MediaFingerprint(context.Background(), path)
				if err != nil {
					t.Fatalf("%s: fingerprint: %v", id, err)
				}
				if representative == "" {
					representative = fingerprint
				} else if fingerprint != representative {
					t.Errorf("%s: fingerprint %s differs from group %s", id, fingerprint, representative)
				}
				file, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				h := sha256.New()
				_, copyErr := io.Copy(h, file)
				closeErr := file.Close()
				if copyErr != nil || closeErr != nil {
					t.Fatalf("%s: hash bytes: copy=%v close=%v", id, copyErr, closeErr)
				}
				var bytesHash [32]byte
				copy(bytesHash[:], h.Sum(nil))
				if earlier, exists := byteHashes[bytesHash]; exists {
					t.Errorf("%s and %s have identical bytes; the regression needs different raw files", earlier, id)
				}
				byteHashes[bytesHash] = id
			}
			fingerprints[group] = representative
		})
	}
	for first, a := range fingerprints {
		for second, b := range fingerprints {
			if first < second && a == b {
				t.Errorf("distinct %s and %s groups collapsed to %s", first, second, a)
			}
		}
	}
}

// historicalMediaPath finds one named MP4 in the explicitly supplied corpus.
func historicalMediaPath(t *testing.T, dirs []string, id string) string {
	t.Helper()
	for _, dir := range dirs {
		path := filepath.Join(dir, id+".mp4")
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	t.Fatalf("missing historical clip %s in %q", id, dirs)
	return ""
}
