// Fixture-generator tests preserve role ambiguity and advancing secondary timers in synthetic controls.
package main

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestControlsAreDistinct ensures bare numbers stay unknown and SVG output remains valid.
func TestControlsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range controls() {
		if seen[c.Name] {
			t.Fatalf("duplicate %s", c.Name)
		}
		seen[c.Name] = true
		if (strings.HasPrefix(c.Name, "ambiguous") || c.AddedLabel != "") && c.GoldPeriod.Valid() {
			t.Fatalf("number manufactured period: %s", c.Name)
		}
		clock, secondary, gold := frame(c, 1)
		if c.Secondary != "" && (!strings.HasSuffix(clock, ":00") || secondary == c.Secondary || gold.StoppageClock == nil) {
			t.Fatalf("secondary did not advance independently: %s", c.Name)
		}
		d := xml.NewDecoder(strings.NewReader(svg(c, clock, secondary)))
		for {
			_, err := d.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("invalid SVG %s: %v", c.Name, err)
			}
		}
	}
}

// TestGenerateRefusesOverwrite checks the output boundary and expected batch artifacts.
func TestGenerateRefusesOverwrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "generated")
	if err := generate(dir); err != nil {
		t.Fatal(err)
	}
	if err := generate(dir); err == nil {
		t.Fatal("overwrote an existing corpus")
	}
	for _, name := range []string{"manifest-1.json", "manifest-2.json", "first_superscript-1.svg", "second_secondary-3.svg"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}
