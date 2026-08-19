// schema.go — the wire contract with the vision model: the per-frame
// observation type the model returns, the JSON Schema that constrains its
// output (structured-output / constrained decoding), and the default
// prompt. Schema + prompt live together because they co-evolve — the prompt
// describes exactly the fields the schema requires. The base contract was
// validated on real prod clips in the 2026-07-28 bake-off (gemma-4-12b +
// Qwen3.5-9B): multi-frame single call, thinking off, 8/8 clips correct. FF-057
// adds the nullable visible-period field. Regression coverage and three
// Abdelkarim/Zizo replays against gemma-4-12b passed on 2026-08-19.
package vision

import "encoding/json"

// FrameObservation is the model's report for ONE frame. Time fields are
// pointers so JSON null (no timer visible) is distinct from a present value.
// The model returns raw scorebug digits; clock.go turns them into minutes.
type FrameObservation struct {
	Soccer        bool    `json:"soccer"`
	Screen        bool    `json:"screen"`
	Clock         *string `json:"clock"`          // main timer "MM:SS" or null
	Period        *Period `json:"period"`         // visible match-period label or null
	Added         *string `json:"added"`          // announced added time "+N" or null
	StoppageClock *string `json:"stoppage_clock"` // stoppage sub-timer "MM:SS" or null
}

// VisionResponse is the top-level model reply: exactly N frames, in the
// order the images were sent (early / middle / late).
type VisionResponse struct {
	Frames []FrameObservation `json:"frames"`
}

// ResponseSchema is the JSON Schema handed to the model as response_format.
// minItems==maxItems==3 forces exactly-3 positional frames; every field is
// required-but-nullable so each frame is fully reported. additionalProperties
// false keeps the model from inventing fields.
var ResponseSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "frames": {
      "type": "array",
      "minItems": 3,
      "maxItems": 3,
      "items": {
        "type": "object",
        "properties": {
          "soccer": {"type": "boolean"},
          "screen": {"type": "boolean"},
          "clock": {"type": ["string", "null"]},
          "period": {"type": ["string", "null"], "enum": ["1H", "2H", "ET1", "ET2", null]},
          "added": {"type": ["string", "null"]},
          "stoppage_clock": {"type": ["string", "null"]}
        },
        "required": ["soccer", "screen", "clock", "period", "added", "stoppage_clock"],
        "additionalProperties": false
      }
    }
  },
  "required": ["frames"],
  "additionalProperties": false
}`)

// DefaultPrompt carries the validated base rubric plus FF-057's visible-period
// instruction. The screen definition (filming a TV vs a clean broadcast/fan-
// of-pitch) and frozen-clock-plus-sub-timer description are load-bearing: terse
// versions caused screen false-positives and dropped the stoppage sub-timer in
// the bake-off. Overridable via config.
const DefaultPrompt = `You are validating a short football (soccer) video clip. Below are 3 still frames from the SAME clip, in chronological order (early, middle, late). For EACH frame, independently report:
- soccer: true ONLY for live ASSOCIATION football (soccer) — players on a pitch, gameplay, a soccer goal, replay, celebration, or VAR. Set false for: any OTHER sport (American football, rugby, futsal, basketball, hockey, etc.); studio/desk, pundits, ads, graphics-only, or anything that isn't live soccer action; AND any NSFW, sexual, gory, graphically violent, or otherwise explicit/disturbing content — such a clip must NEVER pass, so set soccer=false regardless of what else it shows.
- screen: true ONLY if this is a phone/camera pointed at a TV or monitor (moire/scanlines, screen bezel, glare, off-axis tilt, surrounding room). false for a clean broadcast, an app overlay, a scoreboard graphic, or a fan filming the real pitch directly. When in doubt, false.
- clock: the MAIN on-screen match timer as "MM:SS"; null if no timer is visible in that frame.
- period: the match period EXPLICITLY VISIBLE beside the timer, normalized to "1H", "2H", "ET1", or "ET2". Labels such as "1st" and "1H" mean "1H"; "2nd" and "2H" mean "2H". Use "ET1" or "ET2" only when the scorebug explicitly identifies that extra-time period. null if no period label is visible. Do NOT infer the period from the clock value or from the other frames.
- stoppage_clock: during stoppage time the broadcast often FREEZES the main clock at 45:00 or 90:00 and shows a SECOND smaller timer counting the stoppage, e.g. "+1:48"; put that smaller timer here as "MM:SS", or null if there is no second timer.
- added: the announced added-time total, a small number like "+2" or "+5" (NOT a clock); null if not shown.
Read the scorebug carefully for all four timing fields. Return exactly 3 frame objects, in order.`
