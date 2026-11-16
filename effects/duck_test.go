package effects_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/effects"
)

// sliceStreamer is a finite Streamer backed by a slice of samples.
type sliceStreamer struct {
	samples [][2]float64
	pos     int
	err     error
}

func (s *sliceStreamer) Stream(samples [][2]float64) (n int, ok bool) {
	if s.err != nil {
		return 0, false
	}
	if s.pos >= len(s.samples) {
		return 0, false
	}
	n = copy(samples, s.samples[s.pos:])
	s.pos += n
	return n, true
}

func (s *sliceStreamer) Err() error { return s.err }

// constantSamples returns n samples with the given left/right amplitudes.
func constantSamples(n int, left, right float64) [][2]float64 {
	s := make([][2]float64, n)
	for i := range s {
		s[i] = [2]float64{left, right}
	}
	return s
}

// pulseSamples returns n samples which are silent except for pulses of the given
// amplitude. Each pulse starts at every multiple of period and lasts length samples.
func pulseSamples(n, period, length int, amplitude float64) [][2]float64 {
	s := make([][2]float64, n)
	for i := range s {
		if i%period < length {
			s[i] = [2]float64{amplitude, amplitude}
		}
	}
	return s
}

// streamAll drains s into a single slice using blocks of blockSize samples.
func streamAll(s beep.Streamer, blockSize int) [][2]float64 {
	var out [][2]float64
	buf := make([][2]float64, blockSize)
	for {
		n, ok := s.Stream(buf)
		out = append(out, buf[:n]...)
		if !ok {
			return out
		}
	}
}

func TestDuckInvalidParams(t *testing.T) {
	main := &sliceStreamer{samples: constantSamples(16, 0.5, 0.5)}
	side := &sliceStreamer{samples: constantSamples(16, 0.5, 0.5)}
	rate := beep.SampleRate(44100)

	cases := map[string]struct {
		threshold, minGain float64
		attack, release    time.Duration
		rate               beep.SampleRate
		nilMain, nilSide   bool
	}{
		"zero threshold":       {threshold: 0, minGain: 0.1},
		"negative threshold":   {threshold: -0.5, minGain: 0.1},
		"NaN threshold":        {threshold: math.NaN(), minGain: 0.1},
		"infinite threshold":   {threshold: math.Inf(1), minGain: 0.1},
		"negative min gain":    {threshold: 0.2, minGain: -0.1},
		"min gain above one":   {threshold: 0.2, minGain: 1.1},
		"NaN min gain":         {threshold: 0.2, minGain: math.NaN()},
		"infinite min gain":    {threshold: 0.2, minGain: math.Inf(1)},
		"negative attack":      {threshold: 0.2, minGain: 0.1, attack: -time.Millisecond},
		"negative release":     {threshold: 0.2, minGain: 0.1, release: -time.Millisecond},
		"zero sample rate":     {threshold: 0.2, minGain: 0.1, rate: 0},
		"negative sample rate": {threshold: 0.2, minGain: 0.1, rate: -1},
		"nil main":             {threshold: 0.2, minGain: 0.1, nilMain: true},
		"nil sidechain":        {threshold: 0.2, minGain: 0.1, nilSide: true},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var m, s beep.Streamer = main, side
			if c.nilMain {
				m = nil
			}
			if c.nilSide {
				s = nil
			}
			rate := c.rate
			if rate == 0 && name != "zero sample rate" {
				rate = 44100
			}
			d, err := effects.Duck(m, s, rate, c.threshold, c.minGain, c.attack, c.release)
			if err == nil {
				t.Fatalf("expected error, got ducker %+v", d)
			}
		})
	}

	if _, err := effects.Duck(main, side, rate, 0.2, 0.1, time.Millisecond, time.Millisecond); err != nil {
		t.Fatalf("valid parameters rejected: %v", err)
	}
}

// TestDuckBlockSizeIndependence verifies that the sample-level envelope does not
// depend on the requested block sizes.
func TestDuckBlockSizeIndependence(t *testing.T) {
	const total = 5000
	rate := beep.SampleRate(1000)

	newDucker := func() *effects.Ducker {
		main := &sliceStreamer{samples: constantSamples(total, 0.5, -0.25)}
		side := &sliceStreamer{samples: pulseSamples(total, 500, 100, 0.9)}
		d, err := effects.Duck(main, side, rate, 0.2, 0.1, 10*time.Millisecond, 40*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	reference := streamAll(newDucker(), 1)
	if len(reference) != total {
		t.Fatalf("streamed %d samples, want %d", len(reference), total)
	}

	for _, blockSize := range []int{2, 7, 128, 999, 4096} {
		got := streamAll(newDucker(), blockSize)
		if len(got) != len(reference) {
			t.Fatalf("block size %d: streamed %d samples, want %d", blockSize, len(got), len(reference))
		}
		for i := range got {
			if got[i] != reference[i] {
				t.Fatalf("block size %d: sample %d differs: got %v, want %v", blockSize, i, got[i], reference[i])
			}
		}
	}
}

// TestDuckCurve verifies the ducking and recovery curves against the analytic
// one-pole envelope, including the release tail length.
func TestDuckCurve(t *testing.T) {
	rate := beep.SampleRate(1000)
	const (
		mainAmp   = 0.5
		threshold = 0.2
		minGain   = 0.1
		pulseAmp  = 1.0
		silence1  = 100
		pulse     = 400
		silence2  = 600
		attackN   = 10 // samples
		releaseN  = 50 // samples
	)
	total := silence1 + pulse + silence2

	main := &sliceStreamer{samples: constantSamples(total, mainAmp, mainAmp)}
	side := &sliceStreamer{}
	side.samples = append(side.samples, make([][2]float64, silence1)...)
	side.samples = append(side.samples, constantSamples(pulse, pulseAmp, pulseAmp)...)
	side.samples = append(side.samples, make([][2]float64, silence2)...)

	d, err := effects.Duck(main, side, rate, threshold, minGain,
		time.Duration(attackN)*time.Millisecond, time.Duration(releaseN)*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	out := streamAll(d, 64)
	if len(out) != total {
		t.Fatalf("streamed %d samples, want %d", len(out), total)
	}

	// Before the pulse the sidechain is silent: no ducking at all.
	for i := 0; i < silence1; i++ {
		if out[i][0] != mainAmp {
			t.Fatalf("sample %d: silent sidechain must not duck, got %v", i, out[i][0])
		}
	}

	// During the pulse the target gain is threshold/pulseAmp = 0.2 and the envelope
	// follows gain(k) = target + (1-target)*exp(-k/attackN).
	target := threshold / pulseAmp
	for k := 1; k < pulse; k++ {
		want := mainAmp * (target + (1-target)*math.Exp(-float64(k+1)/attackN))
		got := out[silence1+k][0]
		if math.Abs(got-want) > 1e-9 {
			t.Fatalf("pulse sample %d: got %v, want %v (analytic attack curve)", k, got, want)
		}
		if k > 1 && out[silence1+k][0] > out[silence1+k-1][0] {
			t.Fatalf("pulse sample %d: gain must decrease monotonically while ducking", k)
		}
	}

	// After the pulse the gain releases back to unity:
	// gain(k) = 1 - (1-g0)*exp(-k/releaseN), with g0 the gain at the pulse end.
	g0 := out[silence1+pulse-1][0] / mainAmp
	for k := 1; k < silence2; k++ {
		want := mainAmp * (1 - (1-g0)*math.Exp(-float64(k)/releaseN))
		got := out[silence1+pulse+k-1][0]
		if math.Abs(got-want) > 1e-9 {
			t.Fatalf("tail sample %d: got %v, want %v (analytic release curve)", k, got, want)
		}
		if k > 1 && out[silence1+pulse+k-1][0] < out[silence1+pulse+k-2][0] {
			t.Fatalf("tail sample %d: gain must increase monotonically while recovering", k)
		}
	}

	// Tail length: after one release time constant the recovery must still be
	// incomplete (residual ~37%), after five it must be below 1%.
	oneTau := out[silence1+pulse+releaseN-1][0]
	if math.Abs(mainAmp-oneTau) < 0.2*(mainAmp-mainAmp*g0) {
		t.Fatalf("tail too short: after one release constant output is already %v", oneTau)
	}
	fiveTau := out[silence1+pulse+5*releaseN-1][0]
	if math.Abs(mainAmp-fiveTau) > 0.01*mainAmp {
		t.Fatalf("tail too long: after five release constants output is still %v", fiveTau)
	}
}

// TestDuckPreservesStereoRatio verifies both main channels are scaled by the same
// gain, keeping their relative proportion.
func TestDuckPreservesStereoRatio(t *testing.T) {
	rate := beep.SampleRate(1000)
	main := &sliceStreamer{samples: constantSamples(2000, 0.4, 0.1)}
	side := &sliceStreamer{samples: pulseSamples(2000, 400, 200, 1.0)}

	d, err := effects.Duck(main, side, rate, 0.2, 0.1, 5*time.Millisecond, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range streamAll(d, 33) {
		if math.Abs(s[0]-4*s[1]) > 1e-12 {
			t.Fatalf("sample %d: stereo ratio not preserved: %v", i, s)
		}
	}
}

// TestDuckMinGainFloor verifies the gain never drops below minGain, and that a zero
// attack reaches the target within a single sample.
func TestDuckMinGainFloor(t *testing.T) {
	rate := beep.SampleRate(1000)
	main := &sliceStreamer{samples: constantSamples(100, 1.0, 1.0)}
	side := &sliceStreamer{samples: constantSamples(100, 1.0, 1.0)}

	// threshold/level would be 0.2, but minGain 0.5 must clamp it.
	d, err := effects.Duck(main, side, rate, 0.2, 0.5, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := streamAll(d, 100)
	for i, s := range out {
		if s[0] != 0.5 {
			t.Fatalf("sample %d: got %v, want instant ducking to minGain 0.5", i, s[0])
		}
	}
}

// TestDuckSidechainShorter verifies that once the sidechain is drained its level is
// treated as silence: the main stream keeps its own length and the gain releases
// back to unity.
func TestDuckSidechainShorter(t *testing.T) {
	rate := beep.SampleRate(1000)
	const mainLen = 1000
	main := &sliceStreamer{samples: constantSamples(mainLen, 0.5, 0.5)}
	side := &sliceStreamer{samples: constantSamples(100, 1.0, 1.0)} // drains after 100

	d, err := effects.Duck(main, side, rate, 0.2, 0.1, time.Millisecond, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	buf := make([][2]float64, mainLen+500)
	n, ok := d.Stream(buf)
	if n != mainLen || !ok {
		t.Fatalf("first Stream: got n=%d ok=%v, want n=%d ok=true", n, ok, mainLen)
	}
	if n, ok := d.Stream(buf); n != 0 || ok {
		t.Fatalf("drained Stream: got n=%d ok=%v, want n=0 ok=false", n, ok)
	}

	// The end of the stream must be back at (nearly) full volume.
	if got := buf[mainLen-1][0]; math.Abs(got-0.5) > 0.01 {
		t.Fatalf("gain did not recover after sidechain drained: last sample %v", got)
	}
	// And right after the sidechain ended the gain must still be ducked.
	if got := buf[101][0]; got > 0.4 {
		t.Fatalf("expected still-ducked sample right after sidechain end, got %v", got)
	}
}

// TestDuckMainShorter verifies the sidechain is never consumed beyond what the main
// stream produced.
func TestDuckMainShorter(t *testing.T) {
	rate := beep.SampleRate(1000)
	main := &sliceStreamer{samples: constantSamples(50, 0.5, 0.5)}
	side := &sliceStreamer{samples: constantSamples(1000, 1.0, 1.0)}

	d, err := effects.Duck(main, side, rate, 0.2, 0.1, time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	out := streamAll(d, 16)
	if len(out) != 50 {
		t.Fatalf("streamed %d samples, want 50", len(out))
	}
	if side.pos != 50 {
		t.Fatalf("sidechain consumed %d samples, want exactly 50", side.pos)
	}
}

func TestDuckErr(t *testing.T) {
	errMain := errors.New("main failed")
	errSide := errors.New("sidechain failed")
	rate := beep.SampleRate(1000)

	d, err := effects.Duck(
		&sliceStreamer{samples: constantSamples(10, 0.5, 0.5), err: errMain},
		&sliceStreamer{samples: constantSamples(10, 0.5, 0.5)},
		rate, 0.2, 0.1, 0, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := d.Stream(make([][2]float64, 10)); n != 0 || ok {
		t.Fatal("errored main streamer must report drained")
	}
	if !errors.Is(d.Err(), errMain) {
		t.Fatalf("got Err()=%v, want %v", d.Err(), errMain)
	}

	d, err = effects.Duck(
		&sliceStreamer{samples: constantSamples(10, 0.5, 0.5)},
		&sliceStreamer{err: errSide},
		rate, 0.2, 0.1, 0, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(d.Err(), errSide) {
		t.Fatalf("got Err()=%v, want %v", d.Err(), errSide)
	}
}
