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
