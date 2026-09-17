// Generate code-native SVG scorebug controls; diagrams are not natural video evidence.
package main

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"strconv"

	vision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// control fixes literal roles before any model request; Sup makes an ordinal suffix superscript.
type control struct {
	Name, Period, Sup, Added, AddedLabel, Secondary string
	Minute                                          int
	Second                                          int
	GoldPeriod                                      vision.Period
	Expected                                        vision.Expected
}

// controls pairs period variants with numerically similar allowance and unknown-label negatives.
func controls() []control {
	return []control{
		{Name: "first_1st", Period: "1st", Minute: 5, Second: 25, GoldPeriod: vision.PeriodFirstHalf, Expected: vision.Expected{Elapsed: 6}},
		{Name: "second_2nd", Period: "2nd", Minute: 5, Second: 25, GoldPeriod: vision.PeriodSecondHalf, Expected: vision.Expected{Elapsed: 51}},
		{Name: "first_1re", Period: "1re", Minute: 5, Second: 25, GoldPeriod: vision.PeriodFirstHalf, Expected: vision.Expected{Elapsed: 6}},
		{Name: "second_2e", Period: "2e", Minute: 5, Second: 25, GoldPeriod: vision.PeriodSecondHalf, Expected: vision.Expected{Elapsed: 51}},
		{Name: "first_superscript", Period: "1", Sup: "er", Minute: 5, Second: 25, GoldPeriod: vision.PeriodFirstHalf, Expected: vision.Expected{Elapsed: 6}},
		{Name: "second_superscript", Period: "2", Sup: "e", Minute: 5, Second: 25, GoldPeriod: vision.PeriodSecondHalf, Expected: vision.Expected{Elapsed: 51}},
		{Name: "first_half_number", Period: "HALF 1", Minute: 5, Second: 25, GoldPeriod: vision.PeriodFirstHalf, Expected: vision.Expected{Elapsed: 6}},
		{Name: "second_half_number", Period: "HALF 2", Minute: 5, Second: 25, GoldPeriod: vision.PeriodSecondHalf, Expected: vision.Expected{Elapsed: 51}},
		{Name: "ambiguous_one", Period: "1", Minute: 5, Second: 25, Expected: vision.Expected{Elapsed: 6}},
		{Name: "ambiguous_two", Period: "2", Minute: 5, Second: 25, Expected: vision.Expected{Elapsed: 51}},
		{Name: "no_half_label", Minute: 5, Second: 25, Expected: vision.Expected{Elapsed: 51}},
		{Name: "first_added_plus_one", Added: "+1", Minute: 45, Second: 17, Expected: vision.Expected{Elapsed: 45, Extra: 1}},
		{Name: "second_added_plus_two", Added: "+2", Minute: 90, Second: 17, Expected: vision.Expected{Elapsed: 90, Extra: 1}},
		{Name: "first_added_bare_one", Added: "1", AddedLabel: "ADDED TIME", Minute: 45, Expected: vision.Expected{Elapsed: 45, Extra: 1}},
		{Name: "second_added_bare_two", Added: "2", AddedLabel: "ADDED TIME", Minute: 90, Expected: vision.Expected{Elapsed: 90, Extra: 2}},
		{Name: "first_secondary", Added: "+2", Secondary: "01:23", Minute: 45, Expected: vision.Expected{Elapsed: 45, Extra: 2}},
		{Name: "second_secondary", Added: "+2", Secondary: "02:23", Minute: 90, Expected: vision.Expected{Elapsed: 90, Extra: 3}},
		{Name: "reset_half_with_allowance", Period: "2e", Added: "+2", Secondary: "01:23", Minute: 45, GoldPeriod: vision.PeriodSecondHalf, Expected: vision.Expected{Elapsed: 90, Extra: 2}},
		{Name: "first_1P", Period: "1P", Minute: 5, Second: 25, GoldPeriod: vision.PeriodFirstHalf, Expected: vision.Expected{Elapsed: 6}},
		{Name: "second_2P", Period: "2P", Minute: 5, Second: 25, GoldPeriod: vision.PeriodSecondHalf, Expected: vision.Expected{Elapsed: 51}},
		{Name: "first_1p", Period: "1p", Minute: 5, Second: 25, GoldPeriod: vision.PeriodFirstHalf, Expected: vision.Expected{Elapsed: 6}},
		{Name: "second_2p", Period: "2p", Minute: 5, Second: 25, GoldPeriod: vision.PeriodSecondHalf, Expected: vision.Expected{Elapsed: 51}},
	}
}

// stringPointer represents absent annotations as null, never empty timing evidence.
func stringPointer(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// frame returns distinct clock samples and a conservative role annotation for each diagram.
func frame(c control, i int) (string, string, vision.FrameObservation) {
	second := c.Second
	secondary := c.Secondary
	if secondary != "" {
		secondary = secondary[:3] + strconv.Itoa(23+i*3)
	} else if c.Minute != 45 && c.Minute != 90 || c.Second != 0 {
		second += i * 3
	}
	clock := fmt.Sprintf("%02d:%02d", c.Minute, second)
	gold := vision.FrameObservation{Clock: &clock, Added: stringPointer(c.Added), StoppageClock: stringPointer(secondary)}
	if c.GoldPeriod.Valid() {
		p := c.GoldPeriod
		gold.Period = &p
	}
	return clock, secondary, gold
}

// svg constructs a vector diagram; only its period/allowance roles vary between controls.
func svg(c control, clock, secondary string) string {
	period := html.EscapeString(c.Period)
	if c.Sup != "" {
		period += `<tspan dy="-12" font-size="22">` + html.EscapeString(c.Sup) + `</tspan>`
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="960" height="270" viewBox="0 0 960 270">
<rect width="960" height="270" fill="#151923"/>
<g font-family="DejaVu Sans" fill="white">
<rect x="30" y="60" width="880" height="92" rx="4" fill="#28344a"/>
<text x="50" y="119" font-size="40">BAR</text><text x="185" y="119" font-size="40">4 - 1</text>
<text x="320" y="119" font-size="40">RAC</text><text x="445" y="119" font-size="40">%s</text>
<text x="595" y="119" font-size="32">%s</text>
<text x="805" y="83" font-size="12">%s</text><text x="805" y="125" font-size="32">%s</text>
<text x="445" y="196" font-size="32">%s</text>
</g></svg>`, clock, period, html.EscapeString(c.AddedLabel), html.EscapeString(c.Added), secondary)
}

// generate refuses existing output, making every rendered corpus reproducible and non-overwriting.
func generate(dir string) error {
	// #nosec G703 -- Explicit operator-selected output directory; Mkdir refuses existing targets and child names are fixed controls.
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	var batch []map[string]any
	for index, c := range controls() {
		var images []string
		var gold []vision.FrameObservation
		for i := range 3 {
			clock, secondary, observation := frame(c, i)
			base := fmt.Sprintf("%s-%d", c.Name, i+1)
			if err := root.WriteFile(base+".svg", []byte(svg(c, clock, secondary)), 0600); err != nil {
				return err
			}
			images = append(images, "/fixtures/"+base+".png")
			gold = append(gold, observation)
		}
		batch = append(batch, map[string]any{"Name": c.Name, "Provenance": "synthetic SVG role control, not a broadcast; isolated bare numbers deliberately labeled unknown", "Images": images, "Expected": c.Expected, "Gold": gold})
		if len(batch) == 10 || index == len(controls())-1 {
			data, err := json.MarshalIndent(batch, "", "  ")
			if err != nil {
				return err
			}
			if err := root.WriteFile(fmt.Sprintf("manifest-%d.json", index/10+1), data, 0600); err != nil {
				return err
			}
			batch = nil
		}
	}
	return nil
}

// main writes vector sources and manifests; ffmpeg/librsvg performs separate offline rasterization.
func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: fixtures NEW_OUTPUT_DIRECTORY")
		os.Exit(1)
	}
	if err := generate(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
