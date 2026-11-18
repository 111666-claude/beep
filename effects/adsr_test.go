package effects_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/effects"
	"github.com/gopxl/beep/v2/generators"
	"github.com/gopxl/beep/v2/internal/testtools"
)

const adsrEpsilon = 1e-12

// constStreamer streams a constant-amplitude stereo signal. If numSamples is negative, it streams
// forever.
type constStreamer struct {
	l, r       float64
	numSamples int
	pos        int
}

func (c *constStreamer) Stream(samples [][2]float64) (n int, ok bool) {
	for i := range samples {
		if c.numSamples >= 0 && c.pos >= c.numSamples {
			// Pattern 2 of the Streamer contract: a partial final read still returns ok=true;
			// the following call returns 0, false.
			return i, i > 0
		}
		samples[i] = [2]float64{c.l, c.r}
		c.pos++
	}
	return len(samples), true
}

func (c *constStreamer) Err() error { return nil }

type envelopeEvent struct {
	at int
	fn func()
}

// collectWithEvents pulls total samples from the envelope in slices of chunkSize samples and
// fires the given events (sorted by sample position) between Stream calls. Events whose position
// falls inside a chunk are fired at the chunk boundary preceding it; tests that require exact
// intra-chunk timing use driveWithEvents instead.
func collectWithEvents(t *testing.T, a *effects.ADSR, chunkSize, total int, events ...envelopeEvent) [][2]float64 {
	t.Helper()

	out := make([][2]float64, 0, total)
	buf := make([][2]float64, chunkSize)
	for len(out) < total {
		for len(events) > 0 && events[0].at <= len(out) {
			events[0].fn()
			events = events[1:]
		}
		want := min(chunkSize, total-len(out))
		n, ok := a.Stream(buf[:want])
		if !ok {
			t.Fatalf("streamer drained after %d of %d samples", len(out), total)
		}
		out = append(out, buf[:n]...)
	}
	if len(events) != 0 {
		t.Fatalf("%d envelope events were never fired", len(events))
	}
	return out
}

// driveWithEvents pulls the envelope one sample at a time and fires each event immediately before
// the sample with the matching index is scaled, so an event takes effect on that very sample. This
// models a caller invoking Trigger/Release from the speaker thread at an exact sample position and
// is used to prove timing is independent of Stream chunk sizes.
func driveWithEvents(t *testing.T, a *effects.ADSR, total int, events ...envelopeEvent) [][2]float64 {
	t.Helper()

	out := make([][2]float64, total)
	buf := make([][2]float64, 1)
	for i := 0; i < total; i++ {
		for len(events) > 0 && events[0].at <= i {
			events[0].fn()
			events = events[1:]
		}
		_, ok := a.Stream(buf)
		if !ok {
			t.Fatalf("streamer drained after %d of %d samples", i, total)
		}
		out[i] = buf[0]
	}
	if len(events) != 0 {
		t.Fatalf("%d envelope events were never fired", len(events))
	}
	return out
}

// gains extracts the per-sample gain of an envelope output given the constant source amplitudes.
func gains(t *testing.T, out [][2]float64, l, r float64) []float64 {
	t.Helper()

	got := make([]float64, len(out))
	for i, s := range out {
		gl := s[0] / l
		gr := s[1] / r
		if math.Abs(gl-gr) > adsrEpsilon {
			t.Fatalf("sample %d: channels scaled by different gains %v and %v", i, gl, gr)
		}
		got[i] = gl
	}
	return got
}

func assertGainAt(t *testing.T, got []float64, idx int, want float64) {
	t.Helper()
	if math.Abs(got[idx]-want) > adsrEpsilon {
		t.Fatalf("sample %d: expected gain %v, got %v", idx, want, got[idx])
	}
}

func newTestADSR(t *testing.T, s beep.Streamer, sr beep.SampleRate, attack, decay, release time.Duration, sustain float64) *effects.ADSR {
	t.Helper()
	a, err := effects.NewADSR(s, sr, attack, decay, release, sustain)
	if err != nil {
		t.Fatalf("unexpected NewADSR error: %v", err)
	}
	return a
}

// A full note walks through attack, decay, sustain and release with deterministic boundary gains.
func TestADSR_Phases(t *testing.T) {
	const (
		sr             = beep.SampleRate(100)
		l, r           = 0.6, -0.3
		attackSamples  = 3
		decaySamples   = 2
		sustainSamples = 5
		releaseSamples = 4
		sustainLevel   = 0.4
	)
	const releaseAt = attackSamples + decaySamples + sustainSamples // 10
	src := &constStreamer{l: l, r: r, numSamples: -1}
	a := newTestADSR(t, src, sr,
		sr.D(attackSamples), sr.D(decaySamples), sr.D(releaseSamples), sustainLevel)

	if a.Phase() != effects.ADSRIdle || a.Gain() != 0 {
		t.Fatalf("fresh envelope must be idle with zero gain, got phase %v gain %v", a.Phase(), a.Gain())
	}

	// Trigger before sample 0 and Release before sample releaseAt.
	out := driveWithEvents(t, a, releaseAt+releaseSamples+2,
		envelopeEvent{at: 0, fn: a.Trigger},
		envelopeEvent{at: releaseAt, fn: a.Release},
	)
	got := gains(t, out, l, r)

	want := make([]float64, len(out))
	for i := 0; i < attackSamples; i++ {
		want[i] = float64(i) / attackSamples // 0, 1/3, 2/3
	}
	for i := attackSamples; i < attackSamples+decaySamples; i++ {
		p := float64(i-attackSamples) / decaySamples
		want[i] = 1 + (sustainLevel-1)*p // 1.0, 0.7
	}
	for i := attackSamples + decaySamples; i < releaseAt; i++ {
		want[i] = sustainLevel
	}
	for i := releaseAt; i < releaseAt+releaseSamples; i++ {
		p := float64(i-releaseAt) / float64(releaseSamples)
		want[i] = sustainLevel * (1 - p) // 0.4, 0.3, 0.2, 0.1
	}
	for i := releaseAt + releaseSamples; i < len(want); i++ {
		want[i] = 0 // idle again
	}
	for i := range want {
		assertGainAt(t, got, i, want[i])
	}

	if a.Phase() != effects.ADSRIdle || a.Gain() != 0 {
		t.Fatalf("expected idle envelope with zero gain after release, got phase %v gain %v", a.Phase(), a.Gain())
	}
	if err := a.Err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Releasing during the attack ramps to zero from the current level instead of restarting at the
// sustain level.
func TestADSR_EarlyRelease(t *testing.T) {
	const sr = beep.SampleRate(100)
	src := &constStreamer{l: 1, r: 1, numSamples: -1}
	a := newTestADSR(t, src, sr, sr.D(4), sr.D(4), sr.D(4), 0.5)

	// Release right before sample 2 is produced. Samples 0 and 1 are attack samples; the release
	// starts at the current attack level (the next attack gain would be 0.5), not at sustain.
	out := driveWithEvents(t, a, 7,
		envelopeEvent{at: 0, fn: a.Trigger},
		envelopeEvent{at: 2, fn: a.Release},
	)
	got := gains(t, out, 1, 1)
	assertGainAt(t, got, 0, 0)
	assertGainAt(t, got, 1, 0.25)

	// The release of length 4 starts at the held level 0.5 and ends at zero.
	assertGainAt(t, got, 2, 0.5)
	assertGainAt(t, got, 3, 0.375)
	assertGainAt(t, got, 4, 0.25)
	assertGainAt(t, got, 5, 0.125)
	assertGainAt(t, got, 6, 0)
	if a.Phase() != effects.ADSRIdle {
		t.Fatalf("expected idle phase, got %v", a.Phase())
	}

	// Releasing even earlier (right before sample 1) starts the ramp at gain 0.25, proving that an
	// early release continues from the current level instead of jumping to the sustain level.
	out = driveWithEvents(t, a, 6,
		envelopeEvent{at: 0, fn: a.Trigger},
		envelopeEvent{at: 1, fn: a.Release},
	)
	got = gains(t, out, 1, 1)
	assertGainAt(t, got, 0, 0) // sample 0 is the sole attack sample at gain 0
	assertGainAt(t, got, 1, 0.25)
	assertGainAt(t, got, 2, 0.1875)
	assertGainAt(t, got, 3, 0.125)
	assertGainAt(t, got, 4, 0.0625)
	assertGainAt(t, got, 5, 0)
}

// Trigger always restarts the envelope from zero, regardless of the current phase.
func TestADSR_Retrigger(t *testing.T) {
	const sr = beep.SampleRate(100)
	src := &constStreamer{l: 1, r: 1, numSamples: -1}
	a := newTestADSR(t, src, sr, sr.D(4), sr.D(2), sr.D(4), 0.5)

	a.Trigger()
	out := collectWithEvents(t, a, 6, 6) // finishes the attack, two samples into decay
	got := gains(t, out, 1, 1)
	assertGainAt(t, got, 4, 1)    // first decay sample keeps the peak gain 1
	assertGainAt(t, got, 5, 0.75) // decay ramps from 1 towards 0.5

	// Retrigger from the decay: the next sample must start a fresh attack at gain 0.
	a.Trigger()
	if a.Phase() != effects.ADSRAttack {
		t.Fatalf("expected attack phase after retrigger, got %v", a.Phase())
	}
	out = collectWithEvents(t, a, 1, 3)
	got = gains(t, out, 1, 1)
	assertGainAt(t, got, 0, 0)
	assertGainAt(t, got, 1, 0.25)
	assertGainAt(t, got, 2, 0.5)

	// Retrigger from a running release behaves the same way.
	a.Release()
	collectWithEvents(t, a, 1, 2)
	a.Trigger()
	out = collectWithEvents(t, a, 1, 2)
	got = gains(t, out, 1, 1)
	assertGainAt(t, got, 0, 0)
	assertGainAt(t, got, 1, 0.25)
}

// The produced gain sequence must not depend on the Stream chunk size. Events are only observable
// between Stream calls, so the per-sample driver defines the reference sequence and every chunk
// size must reproduce it when the same events are applied at the same sample positions.
func TestADSR_ChunkSizeIndependent(t *testing.T) {
	const (
		sr        = beep.SampleRate(100)
		total     = 90
		releaseAt = 60
	)

	// Reference: one sample per Stream call, events applied at exact sample positions.
	refSrc := &constStreamer{l: 0.7, r: 0.4, numSamples: -1}
	ref := newTestADSR(t, refSrc, sr, sr.D(10), sr.D(10), sr.D(20), 0.25)
	refOut := driveWithEvents(t, ref, total,
		envelopeEvent{at: 0, fn: ref.Trigger},
		envelopeEvent{at: releaseAt, fn: ref.Release},
	)
	reference := gains(t, refOut, 0.7, 0.4)

	// Every chunk size that divides both total and releaseAt fires the events at the same boundary.
	for _, chunk := range []int{1, 2, 3, 5, 6, 10, 15} {
		src := &constStreamer{l: 0.7, r: 0.4, numSamples: -1}
		a := newTestADSR(t, src, sr, sr.D(10), sr.D(10), sr.D(20), 0.25)
		out := collectWithEvents(t, a, chunk, total,
			envelopeEvent{at: 0, fn: a.Trigger},
			envelopeEvent{at: releaseAt, fn: a.Release},
		)
		got := gains(t, out, 0.7, 0.4)
		for i := range reference {
			if math.Abs(reference[i]-got[i]) > adsrEpsilon {
				t.Fatalf("chunk size %d: sample %d differs (reference %v, got %v)",
					chunk, i, reference[i], got[i])
			}
		}
	}

	// Spot-check deterministic boundaries of the reference sequence.
	assertGainAt(t, reference, 0, 0)   // first attack sample
	assertGainAt(t, reference, 9, 0.9) // last attack ramp sample
	assertGainAt(t, reference, 10, 1)  // first decay sample
	assertGainAt(t, reference, 19, 0.325)
	assertGainAt(t, reference, 20, 0.25) // sustain reached
	assertGainAt(t, reference, 59, 0.25)
	assertGainAt(t, reference, 60, 0.25)   // first release sample at the held level
	assertGainAt(t, reference, 61, 0.2375) // descending over 20 samples
	assertGainAt(t, reference, 79, 0.0125)
	assertGainAt(t, reference, 80, 0) // release finished
	assertGainAt(t, reference, 89, 0)
}

// Both channels are scaled by the same gain; the original stream format never changes.
func TestADSR_StereoPassthrough(t *testing.T) {
	const sr = beep.SampleRate(100)
	l, r := 0.6, -0.3
	src := &constStreamer{l: l, r: r, numSamples: -1}
	a := newTestADSR(t, src, sr, sr.D(4), sr.D(2), sr.D(4), 0.5)
	a.Trigger()

	out := collectWithEvents(t, a, 3, 12)
	for i, s := range out {
		g := aGainAt(i)
		if math.Abs(s[0]-l*g) > adsrEpsilon || math.Abs(s[1]-r*g) > adsrEpsilon {
			t.Fatalf("sample %d: expected [%v %v], got [%v %v]", i, l*g, r*g, s[0], s[1])
		}
		if s[1] > 0 {
			t.Fatalf("sample %d: sign of the right channel was altered", i)
		}
		if s[0] < -adsrEpsilon || s[0] > l+adsrEpsilon {
			t.Fatalf("sample %d: gain must stay in [0, 1], got %v", i, s[0]/l)
		}
	}
}

// aGainAt mirrors the expected envelope (A=4, D=2, S=0.5) before any release.
func aGainAt(i int) float64 {
	switch {
	case i < 4:
		return float64(i) / 4
	case i < 6:
		return 1 + (0.5-1)*float64(i-4)/2
	default:
		return 0.5
	}
}

// Zero-duration stages are skipped with deterministic boundary levels.
func TestADSR_ZeroDurations(t *testing.T) {
	const sr = beep.SampleRate(100)

	t.Run("zero attack and decay", func(t *testing.T) {
		src := &constStreamer{l: 1, r: 1, numSamples: -1}
		a := newTestADSR(t, src, sr, 0, 0, sr.D(4), 0.6)
		a.Trigger()
		out := collectWithEvents(t, a, 1, 2)
		got := gains(t, out, 1, 1)
		assertGainAt(t, got, 0, 0.6)
		assertGainAt(t, got, 1, 0.6)
		if a.Phase() != effects.ADSRSustain {
			t.Fatalf("expected sustain phase, got %v", a.Phase())
		}
	})

	t.Run("zero release", func(t *testing.T) {
		src := &constStreamer{l: 1, r: 1, numSamples: -1}
		a := newTestADSR(t, src, sr, sr.D(2), 0, 0, 0.6)
		a.Trigger()
		collectWithEvents(t, a, 1, 4) // past the attack, holding sustain
		a.Release()
		if a.Phase() != effects.ADSRIdle || a.Gain() != 0 {
			t.Fatalf("zero release must go idle immediately, got phase %v gain %v", a.Phase(), a.Gain())
		}
		out := collectWithEvents(t, a, 1, 2)
		got := gains(t, out, 1, 1)
		assertGainAt(t, got, 0, 0)
		assertGainAt(t, got, 1, 0)
	})

	t.Run("all stages zero", func(t *testing.T) {
		src := &constStreamer{l: 1, r: 1, numSamples: -1}
		a := newTestADSR(t, src, sr, 0, 0, 0, 0.6)
		a.Trigger()
		if a.Phase() != effects.ADSRSustain || a.Gain() != 0.6 {
			t.Fatalf("expected immediate sustain at 0.6, got phase %v gain %v", a.Phase(), a.Gain())
		}
		a.Release()
		if a.Phase() != effects.ADSRIdle || a.Gain() != 0 {
			t.Fatalf("expected immediate idle, got phase %v gain %v", a.Phase(), a.Gain())
		}
	})
}

func TestADSR_InvalidParameters(t *testing.T) {
	src := &constStreamer{l: 1, r: 1, numSamples: -1}
	const sr = beep.SampleRate(100)
	okDur := sr.D(1)

	cases := []struct {
		name string
		mod  func(a *effects.ADSR)
		want error
	}{
		{"nil streamer", func(a *effects.ADSR) { a.Streamer = nil }, effects.ErrADSRNilStreamer},
		{"zero sample rate", func(a *effects.ADSR) { a.SampleRate = 0 }, effects.ErrADSRInvalidSampleRate},
		{"negative sample rate", func(a *effects.ADSR) { a.SampleRate = -1 }, effects.ErrADSRInvalidSampleRate},
		{"negative attack", func(a *effects.ADSR) { a.AttackTime = -time.Second }, effects.ErrADSRNegativeDuration},
		{"negative decay", func(a *effects.ADSR) { a.DecayTime = -time.Second }, effects.ErrADSRNegativeDuration},
		{"negative release", func(a *effects.ADSR) { a.ReleaseTime = -time.Second }, effects.ErrADSRNegativeDuration},
		{"sustain below zero", func(a *effects.ADSR) { a.Sustain = -0.01 }, effects.ErrADSRInvalidSustain},
		{"sustain above one", func(a *effects.ADSR) { a.Sustain = 1.01 }, effects.ErrADSRInvalidSustain},
		{"NaN sustain", func(a *effects.ADSR) { a.Sustain = math.NaN() }, effects.ErrADSRInvalidSustain},
	}

	// Constructor rejects everything up front.
	for _, tc := range cases {
		t.Run("NewADSR/"+tc.name, func(t *testing.T) {
			a := &effects.ADSR{
				Streamer:    src,
				SampleRate:  sr,
				AttackTime:  okDur,
				DecayTime:   okDur,
				ReleaseTime: okDur,
				Sustain:     0.5,
			}
			tc.mod(a)
			if _, err := effects.NewADSR(a.Streamer, a.SampleRate, a.AttackTime, a.DecayTime, a.ReleaseTime, a.Sustain); !errors.Is(err, tc.want) {
				t.Fatalf("expected error %v, got %v", tc.want, err)
			}
		})
	}

	// A directly constructed invalid value fails deterministically on first use and stays drained.
	t.Run("direct struct fails on Stream", func(t *testing.T) {
		a := &effects.ADSR{Streamer: src, SampleRate: sr, Sustain: 2}
		n, ok := a.Stream(make([][2]float64, 8))
		if n != 0 || ok {
			t.Fatalf("expected 0, false from invalid envelope, got %d, %v", n, ok)
		}
		if !errors.Is(a.Err(), effects.ErrADSRInvalidSustain) {
			t.Fatalf("expected ErrADSRInvalidSustain, got %v", a.Err())
		}
		n, ok = a.Stream(make([][2]float64, 8))
		if n != 0 || ok {
			t.Fatalf("drained envelope must stay drained, got %d, %v", n, ok)
		}
	})
}

// Before a trigger and after a completed release the envelope passes the source through at zero.
func TestADSR_SilentWhenIdle(t *testing.T) {
	const sr = beep.SampleRate(100)
	src := &constStreamer{l: 0.8, r: 0.4, numSamples: -1}
	a := newTestADSR(t, src, sr, sr.D(2), sr.D(2), sr.D(2), 0.5)

	out := collectWithEvents(t, a, 5, 10)
	for i, s := range out {
		if s != [2]float64{} {
			t.Fatalf("idle envelope must output silence, sample %d is %v", i, s)
		}
	}

	// Release while idle is a no-op.
	a.Release()
	if a.Phase() != effects.ADSRIdle {
		t.Fatalf("Release on idle envelope must be a no-op, got phase %v", a.Phase())
	}

	a.Trigger()
	a.Release()
	collectWithEvents(t, a, 1, 2) // release of length 2: ends at gain 0
	if a.Phase() != effects.ADSRIdle {
		t.Fatalf("expected idle phase, got %v", a.Phase())
	}
	out = collectWithEvents(t, a, 7, 7)
	for i, s := range out {
		if s != [2]float64{} {
			t.Fatalf("envelope must be silent after a completed release, sample %d is %v", i, s)
		}
	}
}

// A silent source still advances the envelope, and once the source drains the envelope drains
// with the standard Streamer return pattern.
func TestADSR_SilentStream(t *testing.T) {
	const sr = beep.SampleRate(100)
	a := newTestADSR(t, generators.Silence(100), sr, sr.D(10), sr.D(10), sr.D(10), 0.5)
	a.Trigger()

	testtools.AssertStreamerHasCorrectReturnBehaviour(t, a, 100)
}

// Errors of the wrapped Streamer are propagated and the envelope becomes drained.
func TestADSR_UnderlyingError(t *testing.T) {
	const sr = beep.SampleRate(100)
	myErr := errors.New("boom")

	t.Run("immediate error", func(t *testing.T) {
		a := newTestADSR(t, testtools.NewErrorStreamer(myErr), sr, sr.D(2), sr.D(2), sr.D(2), 0.5)
		a.Trigger()
		n, ok := a.Stream(make([][2]float64, 8))
		if n != 0 || ok {
			t.Fatalf("expected 0, false, got %d, %v", n, ok)
		}
		if !errors.Is(a.Err(), myErr) {
			t.Fatalf("expected propagated error %v, got %v", myErr, a.Err())
		}
		// Events on a drained envelope are no-ops.
		a.Trigger()
		a.Release()
		n, ok = a.Stream(make([][2]float64, 8))
		if n != 0 || ok {
			t.Fatalf("expected drained envelope, got %d, %v", n, ok)
		}
	})

	t.Run("delayed error", func(t *testing.T) {
		src := testtools.NewDelayedErrorStreamer(
			&constStreamer{l: 1, r: 1, numSamples: -1}, 10, myErr)
		a := newTestADSR(t, src, sr, sr.D(2), sr.D(2), sr.D(2), 0.5)
		a.Trigger()

		out := testtools.CollectNum(10, a)
		if len(out) != 10 {
			t.Fatalf("expected 10 samples before the error, got %d", len(out))
		}
		n, ok := a.Stream(make([][2]float64, 8))
		if n != 0 || ok {
			t.Fatalf("expected 0, false after the error, got %d, %v", n, ok)
		}
		if !errors.Is(a.Err(), myErr) {
			t.Fatalf("expected propagated error %v, got %v", myErr, a.Err())
		}
	})
}

// When the finite source ends mid-envelope, the envelope ends at the same sample without
// inventing extra samples.
func TestADSR_StreamEndsMidEnvelope(t *testing.T) {
	const sr = beep.SampleRate(100)
	src := &constStreamer{l: 1, r: 1, numSamples: 20}
	a := newTestADSR(t, src, sr, sr.D(10), sr.D(10), sr.D(40), 0.5)
	a.Trigger()

	out := testtools.Collect(a)
	if len(out) != 20 {
		t.Fatalf("envelope must end exactly with its source, expected 20 samples, got %d", len(out))
	}
	n, ok := a.Stream(make([][2]float64, 8))
	if n != 0 || ok {
		t.Fatalf("expected drained envelope, got %d, %v", n, ok)
	}
	if a.Err() != nil {
		t.Fatalf("unexpected error: %v", a.Err())
	}
	if a.Gain() != 0 {
		t.Fatalf("drained envelope gain must be 0, got %v", a.Gain())
	}

	// Events after the end of the stream do not revive the envelope.
	a.Trigger()
	a.Release()
	n, ok = a.Stream(make([][2]float64, 8))
	if n != 0 || ok {
		t.Fatalf("drained envelope must not be revivable, got %d, %v", n, ok)
	}
}

// Reset silences the envelope immediately without touching the source's playback position, and a
// following Trigger starts a fresh note from zero.
func TestADSR_Reset(t *testing.T) {
	const sr = beep.SampleRate(100)
	data := make([][2]float64, 40)
	for i := range data {
		data[i] = [2]float64{0.5, 0.25}
	}
	src := testtools.NewDataStreamer(data)
	a := newTestADSR(t, src, sr, sr.D(4), sr.D(2), sr.D(4), 0.5)
	a.Trigger()

	collectWithEvents(t, a, 3, 6)
	a.Reset()
	if a.Phase() != effects.ADSRIdle || a.Gain() != 0 {
		t.Fatalf("expected idle zero-gain envelope after Reset, got phase %v gain %v", a.Phase(), a.Gain())
	}
	if src.Position() != 6 {
		t.Fatalf("Reset must not rewind the source, position is %d", src.Position())
	}

	out := collectWithEvents(t, a, 2, 4)
	for i, s := range out {
		if s != [2]float64{} {
			t.Fatalf("expected silence after Reset, sample %d is %v", i, s)
		}
	}
	if src.Position() != 10 {
		t.Fatalf("silence must still consume source samples, position is %d", src.Position())
	}

	a.Trigger()
	out = collectWithEvents(t, a, 1, 3)
	if out[0] != [2]float64{} {
		t.Fatalf("retriggered envelope must start at gain 0, got %v", out[0])
	}
	if math.Abs(out[1][0]-0.5*0.25) > adsrEpsilon {
		t.Fatalf("expected fresh attack ramp after retrigger, got %v", out[1])
	}
}

// The standard return-pattern contract holds for a triggered envelope over a finite source.
func TestADSR_ReturnBehaviour(t *testing.T) {
	const sr = beep.SampleRate(100)
	src := &constStreamer{l: 1, r: 1, numSamples: 200}
	a := newTestADSR(t, src, sr, sr.D(10), sr.D(10), sr.D(10), 0.5)
	a.Trigger()

	testtools.AssertStreamerHasCorrectReturnBehaviour(t, a, 200)
}

// Every phase has a stable name, including the invalid sentinel of a corrupted value.
func TestADSRPhase_String(t *testing.T) {
	cases := map[effects.ADSRPhase]string{
		effects.ADSRIdle:      "idle",
		effects.ADSRAttack:    "attack",
		effects.ADSRDecay:     "decay",
		effects.ADSRSustain:   "sustain",
		effects.ADSRRelease:   "release",
		effects.ADSRPhase(99): "invalid",
	}
	for phase, want := range cases {
		if got := phase.String(); got != want {
			t.Fatalf("phase %d: expected %q, got %q", phase, want, got)
		}
	}
}

// Err reports a configuration error for a nil streamer even before any streaming happens.
func TestADSR_NilStreamerErr(t *testing.T) {
	a, err := effects.NewADSR(nil, beep.SampleRate(100), 0, 0, 0, 0.5)
	if !errors.Is(err, effects.ErrADSRNilStreamer) {
		t.Fatalf("expected ErrADSRNilStreamer from NewADSR, got %v", err)
	}
	if a != nil {
		t.Fatalf("expected nil envelope on error, got %v", a)
	}

	direct := &effects.ADSR{}
	if !errors.Is(direct.Err(), effects.ErrADSRNilStreamer) {
		t.Fatalf("expected ErrADSRNilStreamer from Err, got %v", direct.Err())
	}
}
