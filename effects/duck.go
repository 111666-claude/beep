package effects

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/gopxl/beep/v2"
)

// Ducker attenuates a main Streamer (e.g. background music) whenever a sidechain
// Streamer (e.g. commentary speech) exceeds a threshold, and smoothly restores the
// original level once the sidechain goes quiet. This is the classic "ducking" effect
// used to keep voice intelligible over music without manual volume rides.
//
// The ducking gain is computed per sample from the combined level of both sidechain
// channels (the peak of |left| and |right|) and applied equally to both main channels,
// so the stereo balance of the main stream is preserved. Because the envelope is
// updated per sample, the resulting gain curve is identical no matter which buffer
// sizes are requested from Stream.
//
// When the sidechain is drained, its level is treated as silence and the gain
// releases back to unity; the main stream alone determines how many samples Stream
// produces. Errors of either stream are reported through Err.
//
// Create a Ducker with Duck, which validates its parameters.
type Ducker struct {
	main      beep.Streamer
	sidechain beep.Streamer

	threshold float64
	minGain   float64
	attack    float64 // per-sample smoothing coefficient while ducking
	release   float64 // per-sample smoothing coefficient while recovering

	gain      float64 // current envelope gain, in (0, 1]
	scDrained bool
	scratch   [][2]float64 // reusable sidechain buffer, at most one request block
}

// Duck returns a Ducker which attenuates main whenever the combined stereo level of
// sidechain exceeds threshold.
//
//   - threshold is the linear sidechain peak level (0, +Inf) at which ducking engages.
//     Above it the target gain is threshold/level, i.e. louder sidechains duck harder.
//   - minGain is the lower bound of the gain applied to main, in [0, 1].
//   - attack is the time constant of the gain reduction when the sidechain gets loud.
//   - release is the time constant of the gain recovery when the sidechain goes quiet.
//
// A zero attack or release makes the respective transition instantaneous.
// sampleRate is used to convert attack and release to per-sample coefficients and
// must match the rate both streamers are played back at.
//
// Duck returns an error for nil streamers, a non-positive sample rate, a non-positive
// or non-finite threshold, a negative, greater-than-one or non-finite minGain, and
// negative attack or release.
func Duck(main, sidechain beep.Streamer, sampleRate beep.SampleRate, threshold, minGain float64, attack, release time.Duration) (*Ducker, error) {
	if main == nil {
		return nil, errors.New("duck: main streamer must not be nil")
	}
	if sidechain == nil {
		return nil, errors.New("duck: sidechain streamer must not be nil")
	}
	if sampleRate <= 0 {
		return nil, fmt.Errorf("duck: sample rate must be positive, got %d", sampleRate)
	}
	if math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold <= 0 {
		return nil, fmt.Errorf("duck: threshold must be positive and finite, got %v", threshold)
	}
	if math.IsNaN(minGain) || math.IsInf(minGain, 0) || minGain < 0 || minGain > 1 {
		return nil, fmt.Errorf("duck: min gain must be finite and within [0, 1], got %v", minGain)
	}
	if attack < 0 {
		return nil, fmt.Errorf("duck: attack must not be negative, got %v", attack)
	}
	if release < 0 {
		return nil, fmt.Errorf("duck: release must not be negative, got %v", release)
	}

	return &Ducker{
		main:      main,
		sidechain: sidechain,
		threshold: threshold,
		minGain:   minGain,
		attack:    smoothingCoefficient(sampleRate, attack),
		release:   smoothingCoefficient(sampleRate, release),
		gain:      1,
	}, nil
}

// smoothingCoefficient converts a time constant into a per-sample one-pole
// smoothing coefficient. A non-positive duration maps to an instantaneous step.
func smoothingCoefficient(sampleRate beep.SampleRate, d time.Duration) float64 {
	samples := sampleRate.N(d)
	if samples <= 0 {
		return 1
	}
	return 1 - math.Exp(-1/float64(samples))
}

// Stream streams the main Streamer attenuated by the sidechain-controlled envelope.
//
// The number of samples and the ok flag are those of the main Streamer. If the
// sidechain runs out of samples first, its remaining level is treated as silence
// and the gain smoothly releases back to unity for the rest of the main stream.
func (d *Ducker) Stream(samples [][2]float64) (n int, ok bool) {
	n, ok = d.main.Stream(samples)
	if n == 0 {
		return n, ok
	}

	if cap(d.scratch) < n {
		d.scratch = make([][2]float64, n)
	}
	side := d.scratch[:n]

	sn := 0
	if !d.scDrained {
		var sok bool
		sn, sok = d.sidechain.Stream(side)
		if sn < n || !sok {
			d.scDrained = true
		}
	}

	for i := 0; i < n; i++ {
		level := 0.0
		if i < sn {
			level = math.Max(math.Abs(side[i][0]), math.Abs(side[i][1]))
		}

		target := 1.0
		if level > d.threshold {
			target = math.Max(d.minGain, d.threshold/level)
		}

		if target < d.gain {
			d.gain += (target - d.gain) * d.attack
		} else {
			d.gain += (target - d.gain) * d.release
		}

		samples[i][0] *= d.gain
		samples[i][1] *= d.gain
	}

	return n, ok
}

// Err returns the first error that occurred in the main or the sidechain Streamer.
func (d *Ducker) Err() error {
	if err := d.main.Err(); err != nil {
		return err
	}
	return d.sidechain.Err()
}
