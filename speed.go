package beep

import (
	"fmt"
	"math"
)

// maxSpeedKeyframePosition is the largest output sample position a keyframe may specify. It is the
// largest integer that a float64 represents exactly. Keyframe positions are converted to float64
// when a ratio is interpolated, so larger positions could not be interpolated sample-accurately.
// It's compared in int64 so the constant is also valid on platforms where int is 32 bits wide.
const maxSpeedKeyframePosition = 1<<53 - 1

// SpeedKeyframe describes the playback speed at one output sample position of a SpeedAutomator.
//
// Position is a position in the automator's output, counted in output samples from the first
// sample, not a position in the source Streamer. The same value is reported by
// SpeedAutomator.Position.
//
// Ratio is the resampling ratio used by ResampleRatio: the source sample rate divided by the
// output sample rate. A ratio of 1 plays the source at its original speed, 2 plays it twice as
// fast (an octave higher), and 0.5 plays it at half speed (an octave lower). Ratios must be
// positive and finite.
//
// Keyframes passed to AutomateSpeed must have strictly increasing positions. Between two
// keyframes the ratio is interpolated linearly, and the ratio of the sample at position p is the
// value of that line at p.
type SpeedKeyframe struct {
	Position int
	Ratio    float64
}

// AutomateSpeed returns a Streamer that plays s with a playback speed that changes over the output
// according to keyframes.
//
// The keyframes must be sorted by strictly increasing Position, otherwise AutomateSpeed returns an
// error. Positions must be non-negative and at most 2^53-1, and ratios must be positive and
// finite. AutomateSpeed also returns an error if no keyframes are given or if quality is invalid.
//
// For output sample i the automator looks up the ratio ρ(i) defined by the keyframes and sets the
// underlying Resampler to it, then emits the sample. Consequently the source position used for
// output sample i is the sum of all previous ratios, exactly the integral of the speed curve over
// the output. The ratio is defined per output sample, so the produced output is the same for every
// Stream block size. Adjacent output samples with the same ratio are streamed with a single ratio
// update, which is equivalent and avoids needless work on constant stretches.
//
// Before the first keyframe the first ratio is used and after the last keyframe the last ratio is
// used, so a curve never leaves the automator without a ratio. When the wrapped Streamer is
// drained, the automator drains as well, even if the keyframe curve has not finished; in that case
// Position reports the number of samples actually produced and is smaller than the last keyframe's
// position. SpeedAutomator.Err propagates errors from s.
//
// For example, the following plays s at half speed and linearly accelerates it to normal speed
// over one second:
//
//	sr := beep.SampleRate(44100)
//	automator, err := beep.AutomateSpeed(4, s,
//		beep.SpeedKeyframe{Position: 0, Ratio: 0.5},
//		beep.SpeedKeyframe{Position: sr.N(time.Second), Ratio: 1},
//	)
//	if err != nil {
//		// handle invalid keyframes
//	}
//	speaker.Play(automator)
//
// The quality argument has the same meaning as in Resample and must be between 1 and 64. Higher
// qualities consume more CPU. Because the ratio changes while streaming, a SpeedAutomator is not
// safe for concurrent use; lock the speaker before reading Position or Ratio from another
// goroutine.
func AutomateSpeed(quality int, s Streamer, keyframes ...SpeedKeyframe) (*SpeedAutomator, error) {
	if quality < 1 || 64 < quality {
		return nil, fmt.Errorf("speed: invalid quality: %d", quality)
	}
	if len(keyframes) == 0 {
		return nil, fmt.Errorf("speed: at least one keyframe is required")
	}

	kfs := make([]SpeedKeyframe, len(keyframes))
	copy(kfs, keyframes)
	for i, kf := range kfs {
		if kf.Ratio <= 0 || math.IsInf(kf.Ratio, 0) || math.IsNaN(kf.Ratio) {
			return nil, fmt.Errorf("speed: keyframe %d: invalid ratio: %v", i, kf.Ratio)
		}
		if kf.Position < 0 {
			return nil, fmt.Errorf("speed: keyframe %d: negative position: %d", i, kf.Position)
		}
		if int64(kf.Position) > maxSpeedKeyframePosition {
			return nil, fmt.Errorf("speed: keyframe %d: position %d exceeds the maximum of %d", i, kf.Position, int64(maxSpeedKeyframePosition))
		}
		if i > 0 && kf.Position <= kfs[i-1].Position {
			return nil, fmt.Errorf("speed: keyframe %d: position %d is not greater than the previous position %d (keyframes must be strictly increasing)", i, kf.Position, kfs[i-1].Position)
		}
	}

	return &SpeedAutomator{
		r:         ResampleRatio(quality, kfs[0].Ratio, s),
		keyframes: kfs,
	}, nil
}

// SpeedAutomator is a Streamer created by AutomateSpeed. It plays a wrapped Streamer at a speed
// that changes over the output according to a set of SpeedKeyframes.
//
// A SpeedAutomator is not a StreamSeeker and doesn't support seeking. It is not safe for
// concurrent use.
type SpeedAutomator struct {
	r         *Resampler
	keyframes []SpeedKeyframe
	seg       int  // index of the keyframe segment used by the last ratio lookup
	pos       int  // number of output samples produced so far
	done      bool // the wrapped Streamer has drained
}

// Stream streams the wrapped Streamer at the speed dictated by the keyframes. The ratio is set for
// every output sample, so the output doesn't depend on the size of the requested blocks.
//
// Stream returns (0, false) once the wrapped Streamer is drained, regardless of whether the
// keyframe curve has finished.
func (a *SpeedAutomator) Stream(samples [][2]float64) (n int, ok bool) {
	if a.done {
		return 0, false
	}

	for len(samples) > 0 {
		ratio, run := a.next(a.pos)
		if run > len(samples) {
			run = len(samples)
		}

		a.r.SetRatio(ratio)
		sn, sok := a.r.Stream(samples[:run])
		if sn > 0 {
			n += sn
			a.pos += sn
			samples = samples[sn:]
		}

		// Resampler returns fewer samples than requested only when the wrapped Streamer has
		// drained. It's also drained when it reports !ok. Either way, no more samples will ever
		// come, so the whole automator becomes drained. Returning all samples produced so far
		// with ok == true follows the Streamer contract: the next call returns (0, false).
		if sn < run || !sok {
			a.done = true
			if n > 0 {
				return n, true
			}
			return 0, false
		}
	}

	return n, true
}

// Err propagates the original Streamer's errors.
func (a *SpeedAutomator) Err() error {
	return a.r.Err()
}

// Position returns the number of output samples produced so far, which is also the output position
// of the next sample and the position the keyframes are looked up at. Position is 0 before the
// first call to Stream.
//
// If the wrapped Streamer drains before the keyframe curve finishes, Position stops at the number
// of samples produced and is smaller than the last keyframe's position.
func (a *SpeedAutomator) Position() int {
	return a.pos
}

// Ratio returns the ratio that will be used for the next output sample, which is the interpolated
// ratio at Position. After the last keyframe this is the last keyframe's ratio. Before the first
// call to Stream it is the ratio at position 0.
func (a *SpeedAutomator) Ratio() float64 {
	ratio, _ := a.next(a.pos)
	return ratio
}

// next returns the ratio for output sample pos and the number of consecutive output samples
// starting at pos for which that ratio is constant. The returned run length is always at least 1
// and at most math.MaxInt.
func (a *SpeedAutomator) next(pos int) (ratio float64, run int) {
	kfs := a.keyframes
	last := len(kfs) - 1

	if pos < kfs[0].Position {
		return kfs[0].Ratio, kfs[0].Position - pos
	}
	if pos >= kfs[last].Position {
		return kfs[last].Ratio, math.MaxInt
	}

	// kfs[0].Position <= pos < kfs[last].Position, so pos lies in some segment and the segment's
	// right keyframe exists. pos only ever grows, so a.seg only needs to move forward.
	for pos >= kfs[a.seg+1].Position {
		a.seg++
	}

	k0, k1 := kfs[a.seg], kfs[a.seg+1]
	if k0.Ratio == k1.Ratio {
		// Linear interpolation between equal ratios is constant; stream the whole segment with a
		// single ratio change. This is exactly equivalent to setting the ratio per sample.
		return k0.Ratio, k1.Position - pos
	}

	frac := float64(pos-k0.Position) / float64(k1.Position-k0.Position)
	return k0.Ratio + (k1.Ratio-k0.Ratio)*frac, 1
}
