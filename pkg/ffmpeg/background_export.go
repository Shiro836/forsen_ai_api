package ffmpeg

// BackgroundAudio returns the embedded companion audio for filters that need
// one: impulse responses for the echo/ghost filters, looped ambience for the
// background filters. Nil for plain filters.
func (t FilterType) BackgroundAudio() []byte {
	return getBackgroundAudio(t)
}
