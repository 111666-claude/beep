package effects

import (
	"errors"
	"time"

	"github.com/gopxl/beep/v2"
)

// ADSRPhase is the phase of an ADSR envelope.
type ADSRPhase int

const (
	// ADSRIdle is the phase before the first Trigger and after a Reset. The level is 0.
	ADSRIdle ADSRPhase = iota
	// ADSRAttack is the phase in which the level rises linearly from 0 to 1.
	ADSRAttack
	// ADSRDecay is the phase in which the level falls linearly from 1 to the sustain level.
	ADSRDecay
	// ADSRSustain is the phase in which the level stays at the sustain level.
	ADSRSustain
	// ADSRRelease is the phase in which the level falls linearly from its current value to 0.
	ADSRRelease
)

// String returns the name of the phase.
func (p ADSRPhase) String() string {
	switch p {
	case ADSRIdle:
		return "idle"
	case ADSRAttack:
		return "attack"
	case ADSRDecay:
		return "decay"
	case ADSRSustain:
		return "sustain"
	case ADSRRelease:
		return "release"
	default:
		return "invalid"
	}
}

var (
	// ErrADSRNegativeDuration is returned when an ADSR stage duration is negative.
	ErrADSRNegativeDuration = errors.New("effects: ADSR stage durations must not be negative")
	// ErrADSRInvalidSustain is returned when the sustain level is outside of [0, 1].
	ErrADSRInvalidSustain = errors.New("effects: ADSR sustain level must be between 0 and 1")
	// ErrADSRInvalidSampleRate is returned when the sample rate is not positive.
	ErrADSRInvalidSampleRate = errors.New("effects: ADSR sample rate must be positive")
	// ErrADSRNilStreamer is returned when the wrapped Streamer is nil.
	ErrADSRNilStreamer = errors.New("effects: ADSR streamer must not be nil")
)

// ADSR is a streaming attack-decay-sustain-release envelope.
//
// It wraps a Streamer and multiplies every streamed sample by a gain in [0, 1]. The envelope is
// driven by two events:
//
//   - Trigger starts a note: the gain ramps from 0 to 1 during Attack, then from 1 to Sustain
//     during Decay, and then stays at Sustain for as long as the note is held. Trigger can be
//     called at any time, including from any other phase: it always restarts the attack from a
//     gain of 0 (hard retrigger, as on a monophonic synthesizer).
//   - Release ends a note: the gain ramps linearly from its current level to 0 over Release and
//     the envelope then becomes idle. If Release is called before the attack or decay has
//     finished, the ramp starts from the current gain instead of the sustain level, so no part of
//     the envelope is skipped or reset. Calling Release while idle or already releasing has no
//     effect.
//
// Before the first Trigger (and after a Release has fully completed or a Reset), the envelope is
// idle and streams the wrapped Streamer multiplied by zero, i.e. silence.
//
// Durations are converted to sample counts using SampleRate. A zero-duration stage is skipped
// entirely: zero Attack starts the decay at gain 1 immediately, zero Decay starts the sustain at
// the sustain level immediately, and zero Release silences the envelope immediately. The gain at
// every boundary sample is deterministic and does not depend on the size of the slices passed to
// Stream, because events are only applied between Stream calls and each sample's gain is computed
// from its own position inside the envelope.
//
// When the wrapped Streamer is drained, the ADSR is drained as well. Any release that has not
// finished at that point is cut short; this keeps the output format identical to the wrapped
// Streamer's and never appends samples the source does not produce. Errors of the wrapped
// Streamer are propagated by Err.
//
// An ADSR value may be created directly; in that case its fields must be valid before the first
// call to Stream. Use NewADSR to validate the fields up front.
type ADSR struct {
	Streamer beep.Streamer

	// SampleRate is the number of samples per second of the wrapped Streamer.
	SampleRate beep.SampleRate
	// AttackTime is the time the gain takes to rise from 0 to 1.
	AttackTime time.Duration
	// DecayTime is the time the gain takes to fall from 1 to Sustain.
	DecayTime time.Duration
	// Sustain is the gain held while the note is held. It must be in [0, 1].
	Sustain float64
	// ReleaseTime is the time the gain takes to fall from its current level to 0 after Release.
	ReleaseTime time.Duration

	phase     ADSRPhase
	level     float64
	samples   int // samples already spent in the current phase
	stageLen  int // total number of samples in the current timed stage
	fromLevel float64
	toLevel   float64
	drained   bool
	err       error
}

// NewADSR validates the parameters and returns a new ADSR envelope wrapping s in its idle phase.
//
// All durations must be non-negative, sustain must be in [0, 1] and the sample rate must be
// positive.
func NewADSR(s beep.Streamer, sampleRate beep.SampleRate, attack, decay, release time.Duration, sustain float64) (*ADSR, error) {
	a := &ADSR{
		Streamer:    s,
		SampleRate:  sampleRate,
		AttackTime:  attack,
		DecayTime:   decay,
		ReleaseTime: release,
		Sustain:     sustain,
		phase:       ADSRIdle,
	}
	if err := a.validate(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *ADSR) validate() error {
	if a.Streamer == nil {
		return ErrADSRNilStreamer
	}
	if a.SampleRate <= 0 {
		return ErrADSRInvalidSampleRate
	}
	if a.AttackTime < 0 || a.DecayTime < 0 || a.ReleaseTime < 0 {
		return ErrADSRNegativeDuration
	}
	if a.Sustain < 0 || a.Sustain > 1 || a.Sustain != a.Sustain { // NaN check
		return ErrADSRInvalidSustain
	}
	return nil
}

// Trigger starts (or restarts) the envelope: the gain immediately begins to rise from 0 to 1
// during the attack stage.
//
// Repeating a Trigger from any phase, including attack, decay, sustain or release, restarts the
// whole envelope from a gain of 0. Trigger on an idle envelope simply starts a new note.
// Triggering a drained envelope has no effect.
func (a *ADSR) Trigger() {
	if a.drained {
		return
	}
	a.enterAttack()
}

// Release moves the envelope into its release stage, ramping the gain from the current level to 0.
//
// If the envelope is idle, already releasing or drained, Release has no effect.
func (a *ADSR) Release() {
	if a.drained || a.phase == ADSRIdle || a.phase == ADSRRelease {
		return
	}
	a.enterRelease(a.level)
}

// Reset stops the envelope immediately: the gain becomes 0 and the phase becomes idle. The
// wrapped Streamer's playback position is not touched. A subsequent Trigger starts a new note
// from gain 0.
func (a *ADSR) Reset() {
	a.phase = ADSRIdle
	a.level = 0
	a.samples = 0
	a.stageLen = 0
	a.fromLevel = 0
	a.toLevel = 0
	// The drained flag and the wrapped Streamer are intentionally left untouched.
}

// Phase returns the current phase of the envelope.
func (a *ADSR) Phase() ADSRPhase {
	return a.phase
}

// Gain returns the current envelope gain in [0, 1], i.e. the gain that will be applied to the
// next streamed sample. It is 0 while the envelope is idle or drained.
func (a *ADSR) Gain() float64 {
	return a.level
}

func (a *ADSR) enterAttack() {
	a.phase = ADSRAttack
	a.samples = 0
	a.stageLen = a.SampleRate.N(a.AttackTime)
	a.fromLevel = 0
	a.toLevel = 1
	a.level = 0
	if a.stageLen == 0 {
		a.enterDecay()
	}
}

func (a *ADSR) enterDecay() {
	a.phase = ADSRDecay
	a.samples = 0
	a.stageLen = a.SampleRate.N(a.DecayTime)
	a.fromLevel = 1
	a.toLevel = a.Sustain
	a.level = 1
	if a.stageLen == 0 {
		a.enterSustain()
	}
}

func (a *ADSR) enterSustain() {
	a.phase = ADSRSustain
	a.samples = 0
	a.stageLen = 0
	a.fromLevel = a.Sustain
	a.toLevel = a.Sustain
	a.level = a.Sustain
}

// enterRelease starts the release ramp from the given start level. A zero-length release reaches
// gain 0 immediately.
func (a *ADSR) enterRelease(from float64) {
	a.phase = ADSRRelease
	a.samples = 0
	a.stageLen = a.SampleRate.N(a.ReleaseTime)
	a.fromLevel = from
	a.toLevel = 0
	a.level = from
	if a.stageLen == 0 {
		a.enterIdle()
	}
}

func (a *ADSR) enterIdle() {
	a.phase = ADSRIdle
	a.samples = 0
	a.stageLen = 0
	a.fromLevel = 0
	a.toLevel = 0
	a.level = 0
}

// advance moves the envelope to the state used for the next sample. The current sample's gain is
// a.level, which is why every stage is entered at its start level before the first sample.
func (a *ADSR) advance() {
	switch a.phase {
	case ADSRAttack, ADSRDecay, ADSRRelease:
		a.samples++
		if a.samples >= a.stageLen {
			switch a.phase {
			case ADSRAttack:
				a.enterDecay()
			case ADSRDecay:
				a.enterSustain()
			case ADSRRelease:
				a.enterIdle()
			}
			return
		}
		p := float64(a.samples) / float64(a.stageLen)
		a.level = a.fromLevel + (a.toLevel-a.fromLevel)*p
	case ADSRSustain:
		a.level = a.Sustain
	case ADSRIdle:
		a.level = 0
	}
}

// Stream streams the wrapped Streamer with every sample multiplied by the current envelope gain.
// Both channels of a sample are scaled by the same gain, so the stream format is unchanged.
func (a *ADSR) Stream(samples [][2]float64) (n int, ok bool) {
	if a.drained {
		return 0, false
	}
	if a.err == nil {
		a.err = a.validate()
	}
	if a.err != nil {
		a.drained = true
		return 0, false
	}

	n, ok = a.Streamer.Stream(samples)
	for i := 0; i < n; i++ {
		g := a.level
		samples[i][0] *= g
		samples[i][1] *= g
		a.advance()
	}
	if !ok {
		// The wrapped Streamer is drained (or reported an error). The envelope must not invent
		// additional samples, so it becomes drained as well.
		a.drained = true
		a.level = 0
	}
	return n, ok
}

// Err returns the first error that occurred: an invalid configuration or an error propagated from
// the wrapped Streamer.
func (a *ADSR) Err() error {
	if a.err != nil {
		return a.err
	}
	if a.Streamer == nil {
		return ErrADSRNilStreamer
	}
	return a.Streamer.Err()
}
