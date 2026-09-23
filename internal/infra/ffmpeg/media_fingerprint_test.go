// media_fingerprint_test.go — media-identity invariants without external tools.
package ffmpeg

import (
	"context"
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
	first, err := fingerprintFromFake(t, fingerprintStreams, fingerprintPacketsA)
	if err != nil {
		t.Fatal(err)
	}
	otherTags := strings.Replace(fingerprintStreams, "different upload title", "new metadata only", 1)
	otherTags = strings.Replace(otherTags, "tomorrow", "yesterday", 1)
	interleaved := strings.Replace(fingerprintPacketsA,
		`{"stream_index":1,"pts_time":"0.000000","dts_time":"0.000000","duration_time":"0.020000","size":"2","data_hash":"SHA256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	{"stream_index":0,"pts_time":"0.040000"`,
		`{"stream_index":0,"pts_time":"0.040000"`, 1)
	interleaved = strings.Replace(interleaved, `"data_hash":"SHA256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}`,
		`"data_hash":"SHA256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
	{"stream_index":1,"pts_time":"0.000000","dts_time":"0.000000","duration_time":"0.020000","size":"2","data_hash":"SHA256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`, 1)
	second, err := fingerprintFromFake(t, otherTags, interleaved)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !strings.HasPrefix(first, MediaFingerprintVersion+":") {
		t.Fatalf("metadata/interleave changed identity: %q != %q", first, second)
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
