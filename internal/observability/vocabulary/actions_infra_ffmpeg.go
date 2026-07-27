// FFmpeg adapter Action enum values + init-time registration.
package vocabulary

const (
	ActionFFmpegProbe     Action = "ffmpeg_probe"     // ffprobe metadata read ok
	ActionFFmpegExtract   Action = "ffmpeg_extract"   // frame extraction ok (single or dense)
	ActionFFmpegFaststart Action = "ffmpeg_faststart" // moov-atom faststart remux ok
	ActionFFmpegOpFailed  Action = "ffmpeg_op_failed" // any op failed (typed error attached)
)

func init() {
	registerActions(
		ActionFFmpegProbe,
		ActionFFmpegExtract,
		ActionFFmpegFaststart,
		ActionFFmpegOpFailed,
	)
}
