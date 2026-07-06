// Package vision owns AI validation: frame extraction via ffmpeg, LLM
// vision classification (SOCCER/SCREEN + clock extraction), timestamp
// verification against API-reported match minute. Also owns perceptual
// hashing (dHash + dense sampling). See §4 vision domain + §7 pipeline.
package vision
