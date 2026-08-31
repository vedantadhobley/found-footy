// input.go — CSV decoding for the FF-081 retained video-quality corpus.
package main

import (
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// asset is one retained video asset plus the event/share facts needed to
// reproduce category-scoped matching and explain an audit finding.
type asset struct {
	eventID            string
	id                 string
	firstSeenAt        string
	md5                string
	hashVersion        dvideo.FrameHashVersion
	frameHashes        []uint64
	width              int
	height             int
	durationMS         int
	fileSizeBytes      int64
	bitrate            int
	frameRate          float64
	popularity         int
	observedPopularity int
	supersededBy       string
	verified           bool
	shareState         string
	shareID            string
	fixtureID          int64
	playerName         string
	minute             int
	extra              string
	homeTeam           string
	awayTeam           string
	sourceTweetURL     string
}

// quality projects retained metadata through the production keeper policy.
func (a asset) quality() dvideo.ClipQuality {
	var bitrate *int
	if a.bitrate > 0 {
		value := a.bitrate
		bitrate = &value
	}
	return dvideo.ClipQuality{
		DurationMS: a.durationMS,
		Bitrate:    bitrate,
		Width:      a.width,
		Height:     a.height,
	}
}

// spatialBitrateDensity reproduces the current keeper signal for reports and
// policy simulations. Its unit is bits per spatial pixel per second.
func (a asset) spatialBitrateDensity() float64 {
	if a.bitrate <= 0 || a.width <= 0 || a.height <= 0 {
		return 0
	}
	return float64(a.bitrate) / float64(a.width*a.height)
}

// bitsPerPixelFrame reports the compression budget per spatial pixel per
// encoded frame. It is evidence beside frame rate, not a complete quality
// score: higher cadence can remain preferable at a lower per-frame budget.
func (a asset) bitsPerPixelFrame() float64 {
	if a.frameRate <= 0 {
		return 0
	}
	return a.spatialBitrateDensity() / a.frameRate
}

// readAssets decodes query.sql's header-addressed CSV stream.
func readAssets(r io.Reader) ([]asset, error) {
	reader := csv.NewReader(r)
	reader.ReuseRecord = true
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	columns := make(map[string]int, len(header))
	for i, name := range header {
		columns[name] = i
	}
	required := []string{
		"event_id", "asset_id", "first_seen_at", "md5_hex", "hash_version",
		"frame_hashes_hex", "width", "height", "duration_ms", "file_size_bytes",
		"bitrate", "popularity", "superseded_by", "timestamp_verified",
		"share_state", "share_id", "fixture_id", "player_name", "minute",
		"extra", "home_team_name", "away_team_name",
	}
	for _, name := range required {
		if _, ok := columns[name]; !ok {
			return nil, fmt.Errorf("missing column %q", name)
		}
	}

	var assets []asset
	for rowNumber := 2; ; rowNumber++ {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read row %d: %w", rowNumber, err)
		}
		parsed, err := parseAsset(record, columns)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", rowNumber, err)
		}
		assets = append(assets, parsed)
	}
	return assets, nil
}

// parseAsset validates and converts one corpus row.
func parseAsset(record []string, columns map[string]int) (asset, error) {
	value := func(name string) string { return record[columns[name]] }
	optionalValue := func(name string) string {
		index, found := columns[name]
		if !found || index >= len(record) {
			return ""
		}
		return record[index]
	}
	parseInt := func(name string) (int, error) {
		parsed, err := strconv.Atoi(value(name))
		if err != nil {
			return 0, fmt.Errorf("parse %s: %w", name, err)
		}
		return parsed, nil
	}
	parseInt64 := func(name string) (int64, error) {
		parsed, err := strconv.ParseInt(value(name), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse %s: %w", name, err)
		}
		return parsed, nil
	}

	width, err := parseInt("width")
	if err != nil {
		return asset{}, err
	}
	height, err := parseInt("height")
	if err != nil {
		return asset{}, err
	}
	durationMS, err := parseInt("duration_ms")
	if err != nil {
		return asset{}, err
	}
	fileSizeBytes, err := parseInt64("file_size_bytes")
	if err != nil {
		return asset{}, err
	}
	bitrate, err := parseInt("bitrate")
	if err != nil {
		return asset{}, err
	}
	frameRate := 0.0
	if raw := optionalValue("frame_rate"); raw != "" {
		frameRate, err = strconv.ParseFloat(raw, 64)
		if err != nil {
			return asset{}, fmt.Errorf("parse frame_rate: %w", err)
		}
	}
	popularity, err := parseInt("popularity")
	if err != nil {
		return asset{}, err
	}
	observedPopularity := 0
	if raw := optionalValue("observed_popularity"); raw != "" {
		observedPopularity, err = strconv.Atoi(raw)
		if err != nil {
			return asset{}, fmt.Errorf("parse observed_popularity: %w", err)
		}
	}
	verified, err := strconv.ParseBool(value("timestamp_verified"))
	if err != nil {
		return asset{}, fmt.Errorf("parse timestamp_verified: %w", err)
	}
	fixtureID, err := parseInt64("fixture_id")
	if err != nil {
		return asset{}, err
	}
	minute, err := parseInt("minute")
	if err != nil {
		return asset{}, err
	}
	hashes, err := decodeFrameHashes(value("frame_hashes_hex"))
	if err != nil {
		return asset{}, err
	}

	return asset{
		eventID: value("event_id"), id: value("asset_id"),
		firstSeenAt: value("first_seen_at"), md5: value("md5_hex"),
		hashVersion: dvideo.NormalizeFrameHashVersion(dvideo.FrameHashVersion(value("hash_version"))),
		frameHashes: hashes, width: width, height: height, durationMS: durationMS,
		fileSizeBytes: fileSizeBytes, bitrate: bitrate, frameRate: frameRate, popularity: popularity,
		observedPopularity: observedPopularity,
		supersededBy:       value("superseded_by"), verified: verified,
		shareState: value("share_state"), shareID: value("share_id"),
		fixtureID: fixtureID, playerName: value("player_name"), minute: minute,
		extra: value("extra"), homeTeam: value("home_team_name"),
		awayTeam: value("away_team_name"), sourceTweetURL: optionalValue("source_tweet_url"),
	}, nil
}

// decodeFrameHashes converts Postgres' big-endian BYTEA representation into
// the sequence consumed by the production matcher.
func decodeFrameHashes(encoded string) ([]uint64, error) {
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode frame_hashes_hex: %w", err)
	}
	if len(raw) == 0 || len(raw)%8 != 0 {
		return nil, fmt.Errorf("frame_hashes_hex has %d bytes, want a non-zero multiple of 8", len(raw))
	}
	hashes := make([]uint64, len(raw)/8)
	for i := range hashes {
		hashes[i] = binary.BigEndian.Uint64(raw[i*8:])
	}
	return hashes, nil
}
