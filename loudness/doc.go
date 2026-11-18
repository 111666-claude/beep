// Package loudness provides a streaming loudness meter implementing the
// ITU-R BS.1770-4 recommendation (EBU R 128).
//
// The Meter wraps a beep.Streamer, passes its samples through unchanged and
// simultaneously computes momentary (400 ms), short-term (3 s) and integrated
// (whole-stream, gated) loudness in LUFS. Measurements are independent of the
// chunk sizes used when calling Stream and the per-stream memory usage is
// bounded, regardless of how long the wrapped Streamer runs.
package loudness
