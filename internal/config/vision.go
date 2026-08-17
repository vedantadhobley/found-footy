// vision.go — VisionConfig for the clip-validation activity
// (soccer/screen gate + clock verification via a vision LLM). The model
// endpoint + model ID come from the shared LLMConfig (point LLM_ENDPOINT_URL
// at the vision node, e.g. nexus/gemma-4-12b); this struct holds only the
// vision-specific knobs. Defaults are the values locked in the 2026-07-28
// gemma/Qwen bake-off. All env-tunable so we retune on real match data
// without a rebuild.
package config

// VisionConfig configures internal/activity/vision.
type VisionConfig struct {
	// Prompt overrides the built-in domain DefaultPrompt. Empty = use the
	// validated default (the screen definition + frozen-clock/sub-timer
	// description are load-bearing — see domain/vision/schema.go).
	Prompt string `env:"VISION_PROMPT"`

	// ToleranceMinutes is the ± window for the clock↔API-minute match.
	// 1 validated on real clips; do NOT loosen to 2 (two goals can be a
	// minute apart, and ±2 starts matching the neighboring goal).
	ToleranceMinutes int `env:"VISION_TOLERANCE_MINUTES" envDefault:"1"`

	// FrameQuality is the ffmpeg JPEG quality (-q:v) for the extracted
	// frames. 3 = high (matches the probe frames used in the bake-off).
	FrameQuality int `env:"VISION_FRAME_QUALITY" envDefault:"3"`

	// DisableThinking turns off the model's chain-of-thought. True cut
	// latency ~3x (34s→~13s gemma) with no accuracy loss under the schema
	// constraint. Off (false) only for debugging model reasoning.
	DisableThinking bool `env:"VISION_DISABLE_THINKING" envDefault:"true"`

	// Temperature for the vision call. 0 = deterministic (what we ship).
	Temperature float64 `env:"VISION_TEMPERATURE" envDefault:"0"`
}
