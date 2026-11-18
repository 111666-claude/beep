package loudness_test

import (
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/internal/testtools"
	"github.com/gopxl/beep/v2/loudness"
)

// sineSamples synthesizes n samples of a sine wave with the given peak
// amplitude. When bothChannels is false, the tone is placed on the left
// channel only to exercise stereo/mono handling.
func sineSamples(sr beep.SampleRate, freq, amp float64, n int, bothChannels bool) [][2]float64 {
	data := make([][2]float64, n)
	for i := range data {
		v := amp * math.Sin(2*math.Pi*freq*float64(i)/float64(sr))
		data[i][0] = v
		if bothChannels {
			data[i][1] = v
		}
	}
	return data
}

// drain reads the whole streamer in chunks of chunkLen and returns the number
// of streamed samples.
func drain(t *testing.T, s beep.Streamer, chunkLen int) int {
	t.Helper()
	buf := make([][2]float64, chunkLen)
	total := 0
	for {
		n, ok := s.Stream(buf)
		total += n
		if !ok {
			return total
		}
	}
}

// TestMeter_SineLoudness calibrates the meter against the analytical
// BS.1770-4 K-weighted response of a constant-amplitude sine wave:
//
//	FS stereo 1 kHz ≈ 0 LUFS, mono is exactly 3.01 LU quieter,
//	halving the amplitude shifts loudness by 20*log10(0.5) ≈ -6.02 LU.
func TestMeter_SineLoudness(t *testing.T) {
	// EBU R 128 tolerances are ±0.1 LU; allow a slightly wider margin at the
	// lowest sample rate where the biquad mapping loses accuracy.
	cases := []struct {
		name      string
		sr        beep.SampleRate
		tolerance float64
	}{
		{"48kHz", beep.SampleRate(48000), 0.1},
		{"44.1kHz", beep.SampleRate(44100), 0.1},
		{"22.05kHz", beep.SampleRate(22050), 0.15},
		{"8kHz", beep.SampleRate(8000), 0.3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := int(tc.sr) * 5 // 5 s, an exact number of 100 ms blocks

			// Full-scale stereo sine.
			m, err := loudness.NewMeter(tc.sr, testtools.NewDataStreamer(
				sineSamples(tc.sr, 997, 1.0, n, true)))
			require.NoError(t, err)
			drain(t, m, 512)

			assert.InDelta(t, 0.0, m.Momentary(), tc.tolerance)
			assert.InDelta(t, 0.0, m.ShortTerm(), tc.tolerance)
			assert.InDelta(t, 0.0, m.Integrated(), tc.tolerance)
			assert.False(t, math.IsInf(m.Momentary(), -1))

			// Full-scale mono (left-only) sine: exactly 3.01 LU quieter.
			mono, err := loudness.NewMeter(tc.sr, testtools.NewDataStreamer(
				sineSamples(tc.sr, 997, 1.0, n, false)))
			require.NoError(t, err)
			drain(t, mono, 512)
			assert.InDelta(t, m.Integrated()-3.010, mono.Integrated(), 0.02)

			// Half-amplitude stereo sine: 6.02 LU quieter.
			quiet, err := loudness.NewMeter(tc.sr, testtools.NewDataStreamer(
				sineSamples(tc.sr, 997, 0.5, n, true)))
			require.NoError(t, err)
			drain(t, quiet, 512)
			assert.InDelta(t, m.Integrated()-20.0*math.Log10(2.0), quiet.Integrated(), 0.02)
		})
	}
}

// TestMeter_StepWaveform feeds a 20 dB step: two seconds of full-scale tone
// followed by four seconds at amplitude 0.1.
//
// Momentary loudness must track the step exactly (-20 LU). Integrated
// loudness must remain pinned to the loud section, because the quiet section
// falls 10 LU below the ungated mean and is removed by the relative gate.
func TestMeter_StepWaveform(t *testing.T) {
	sr := beep.SampleRate(44100)
	loudPart := sineSamples(sr, 997, 1.0, 2*int(sr), true)
	quietPart := sineSamples(sr, 997, 0.1, 4*int(sr), true)
	data := append(loudPart, quietPart...)

	m, err := loudness.NewMeter(sr, testtools.NewDataStreamer(data))
	require.NoError(t, err)

	buf := make([][2]float64, sr.N(100*1e6)) // 100 ms per call = exactly one block
	read := 0
	var (
		momAt1s, momAt6s, intAt1s, intAt6s float64
	)
	for {
		n, ok := m.Stream(buf)
		read += n
		if !ok {
			break
		}
		switch read {
		case int(sr):
			momAt1s = m.Momentary()
			intAt1s = m.Integrated()
			// 1 s < 3 s: no short-term window yet.
			assert.True(t, math.IsInf(m.ShortTerm(), -1))
		case 6 * int(sr):
			momAt6s = m.Momentary()
			intAt6s = m.Integrated()
		}
	}

	// Before the step: ≈ 0 LUFS; after a 20 dB step: ≈ -20 LUFS.
	assert.InDelta(t, 0.0, momAt1s, 0.1)
	assert.InDelta(t, -20.0, momAt6s, 0.1)
	// Relative gate removes the quiet section: integrated loudness is
	// unchanged by the step.
	assert.InDelta(t, 0.0, intAt1s, 0.1)
	assert.InDelta(t, intAt1s, intAt6s, 0.05)
	// With 4 s of quiet material, the 3 s short-term window has fully rolled
	// over and reflects only the lower level — proving the ring buffer
	// forgets samples older than its fixed capacity.
	assert.InDelta(t, -20.0, m.ShortTerm(), 0.1)
}

// TestMeter_Silence covers all-silent streams and tone-then-silence streams.
// Silence never contributes to any measurement; integrated loudness stays
// pinned to the audible content and missing windows report SilenceLUFS.
func TestMeter_Silence(t *testing.T) {
	sr := beep.SampleRate(48000)

	t.Run("all silence", func(t *testing.T) {
		m, err := loudness.NewMeter(sr, testtools.NewDataStreamer(
			make([][2]float64, 5*int(sr))))
		require.NoError(t, err)
		drain(t, m, 1024)

		assert.True(t, math.IsInf(m.Momentary(), -1))
		assert.True(t, math.IsInf(m.ShortTerm(), -1))
		assert.True(t, math.IsInf(m.Integrated(), -1))
		assert.Equal(t, loudness.SilenceLUFS, m.Momentary())
	})

	t.Run("tone followed by silence", func(t *testing.T) {
		tone := sineSamples(sr, 997, 1.0, 2*int(sr), true)
		silent := make([][2]float64, 4*int(sr))
		data := append(tone, silent...)
		m, err := loudness.NewMeter(sr, testtools.NewDataStreamer(data))
		require.NoError(t, err)
		drain(t, m, 777)

		// After 4 s of pure silence, both fixed-size windows have fully
		// rolled over to silence blocks.
		assert.True(t, math.IsInf(m.Momentary(), -1))
		assert.True(t, math.IsInf(m.ShortTerm(), -1))
		// Integrated loudness is unaffected by the trailing silence: silence
		// blocks fail the -70 LUFS absolute gate and never enter the
		// histogram.
		assert.InDelta(t, 0.0, m.Integrated(), 0.1)
	})

	t.Run("short stream under one block", func(t *testing.T) {
		// Fewer than 100 ms: no gating block can ever be completed.
		m, err := loudness.NewMeter(sr, testtools.NewDataStreamer(
			sineSamples(sr, 997, 1.0, 123, true)))
		require.NoError(t, err)
		drain(t, m, 37)
		assert.True(t, math.IsInf(m.Momentary(), -1))
		assert.True(t, math.IsInf(m.ShortTerm(), -1))
		assert.True(t, math.IsInf(m.Integrated(), -1))
	})

	t.Run("empty stream", func(t *testing.T) {
		m, err := loudness.NewMeter(sr, testtools.NewDataStreamer(nil))
		require.NoError(t, err)
		n, ok := m.Stream(make([][2]float64, 64))
		assert.Equal(t, 0, n)
		assert.False(t, ok)
		assert.True(t, math.IsInf(m.Integrated(), -1))
		assert.NoError(t, m.Err())
	})
}

// TestMeter_Passthrough verifies the wrapper never alters samples and returns
// the same n, ok as the wrapped Streamer, including the final partial read.
func TestMeter_Passthrough(t *testing.T) {
	numSamples := 1000
	streamer, data := testtools.RandomDataStreamer(numSamples)
	m, err := loudness.NewMeter(beep.SampleRate(44100), streamer)
	require.NoError(t, err)

	out := make([][2]float64, 128)
	collected := make([][2]float64, 0, numSamples)
	for {
		n, ok := m.Stream(out)
		collected = append(collected, out[:n]...)
		if !ok {
			break
		}
	}
	testtools.AssertSamplesEqual(t, data, collected)
	assert.NoError(t, m.Err())
}

// TestMeter_ChunkIndependence streams identical data with wildly different
// chunk sizes and requires bit-identical loudness results: the measurement
// must not depend on how Stream calls are partitioned.
func TestMeter_ChunkIndependence(t *testing.T) {
	sr := beep.SampleRate(44100)
	data := sineSamples(sr, 997, 0.7, 5*int(sr), true)

	reference := func(chunk int) (mom, short, integrated float64) {
		m, err := loudness.NewMeter(sr, testtools.NewDataStreamer(
			append([][2]float64(nil), data...)))
		require.NoError(t, err)
		drain(t, m, chunk)
		return m.Momentary(), m.ShortTerm(), m.Integrated()
	}

	refMom, refShort, refInt := reference(4096)
	for _, chunk := range []int{1, 3, 7, 47, 100, 4410, 4096, 65536} {
		mom, short, integrated := reference(chunk)
		assert.InDelta(t, refMom, mom, 1e-12, "chunk %d momentary", chunk)
		assert.InDelta(t, refShort, short, 1e-12, "chunk %d short-term", chunk)
		assert.InDelta(t, refInt, integrated, 1e-12, "chunk %d integrated", chunk)
	}
}

// TestMeter_BoundedWindows checks the fixed-size ring buffers and the
// integrated-loudness histogram:
//
//   - the short-term window must forget samples older than its fixed 30
//     blocks and end up reflecting only the most recent 3 s;
//   - the histogram instead keeps the whole stream, but the relative gate
//     removes blocks more than 10 LU below the ungated mean.
func TestMeter_BoundedWindows(t *testing.T) {
	sr := beep.SampleRate(48000)
	// Quiet section at -6 LUFS: above the relative gate (≈ -9 LUFS), so it
	// survives into the integrated measurement.
	loudPart := sineSamples(sr, 997, 1.0, 4*int(sr), true)
	quietPart := sineSamples(sr, 997, 0.5, 4*int(sr), true)
	data := append(loudPart, quietPart...)

	m, err := loudness.NewMeter(sr, testtools.NewDataStreamer(data))
	require.NoError(t, err)
	drain(t, m, 2048)

	// The last 3 s window is entirely inside the quiet section, proving the
	// ring buffer discarded the loud section once it aged out of its fixed
	// 30-block capacity.
	assert.InDelta(t, -6.02, m.Momentary(), 0.15)
	assert.InDelta(t, -6.02, m.ShortTerm(), 0.15)

	// Both sections clear the relative gate, so integrated loudness is the
	// energy-weighted blend of equal-length 0 LUFS and -6 LUFS sections:
	// 10*log10((1 + 0.25)/2) ≈ -2.04 LUFS (before the K-weighting constant).
	integrated := m.Integrated()
	assert.False(t, math.IsInf(integrated, -1))
	assert.InDelta(t, -2.04, integrated, 0.3)
	// And it must strictly lie between the two section levels.
	assert.Less(t, integrated, -0.5)
	assert.Greater(t, integrated, -5.5)
}

// TestMeter_RelativeGateRemovesQuietSection confirms the other branch of the
// relative gate: a section 20 LU below the main content is removed from the
// integrated measurement entirely, so integrated loudness equals the loud
// section alone even after the quiet section has fully entered the histogram.
func TestMeter_RelativeGateRemovesQuietSection(t *testing.T) {
	sr := beep.SampleRate(48000)
	loudPart := sineSamples(sr, 997, 1.0, 4*int(sr), true)
	quietPart := sineSamples(sr, 997, 0.1, 4*int(sr), true) // -20 LU
	data := append(loudPart, quietPart...)

	m, err := loudness.NewMeter(sr, testtools.NewDataStreamer(data))
	require.NoError(t, err)
	drain(t, m, 2048)

	// The sliding window does see the quiet content.
	assert.InDelta(t, -20.0, m.ShortTerm(), 0.15)
	// The histogram's relative gate rejects it: integrated loudness stays at
	// the loud-section level.
	assert.InDelta(t, 0.0, m.Integrated(), 0.15)
}

// TestMeter_Reset verifies that Reset restores the initial, deterministic
// state without touching the wrapped Streamer, and measurements then start
// afresh from the next streamed sample.
func TestMeter_Reset(t *testing.T) {
	sr := beep.SampleRate(48000)
	total := 6 * int(sr)
	src, data := testtools.RandomDataStreamer(total)
	m, err := loudness.NewMeter(sr, src)
	require.NoError(t, err)

	buf := make([][2]float64, 1024)
	for i := 0; i < 10; i++ {
		n, ok := m.Stream(buf)
		require.True(t, ok)
		require.Equal(t, 1024, n)
	}
	positionBefore := src.Position()
	require.Equal(t, 10*1024, positionBefore)

	m.Reset()
	assert.True(t, math.IsInf(m.Momentary(), -1))
	assert.True(t, math.IsInf(m.ShortTerm(), -1))
	assert.True(t, math.IsInf(m.Integrated(), -1))
	// The wrapped Streamer position is untouched by Reset.
	assert.Equal(t, positionBefore, src.Position())

	// Replay the same data from the current position and confirm the meter
	// still passes samples through verbatim after a reset.
	out := testtools.Collect(m)
	for i, got := range out {
		want := data[positionBefore+i]
		assert.InDelta(t, want[0], got[0], 1e-12)
		assert.InDelta(t, want[1], got[1], 1e-12)
	}
	assert.Equal(t, total-positionBefore, len(out))
}

// TestMeter_ErrorPropagation ensures an underlying error surfaces through Err
// while the Stream contract (drained forever afterwards) is preserved.
func TestMeter_ErrorPropagation(t *testing.T) {
	sentinel := errors.New("boom")
	sr := beep.SampleRate(48000)

	t.Run("immediate error", func(t *testing.T) {
		m, err := loudness.NewMeter(sr, testtools.NewErrorStreamer(sentinel))
		require.NoError(t, err)

		buf := make([][2]float64, 256)
		n, ok := m.Stream(buf)
		assert.Equal(t, 0, n)
		assert.False(t, ok)
		n, ok = m.Stream(buf)
		assert.Equal(t, 0, n)
		assert.False(t, ok)
		assert.ErrorIs(t, m.Err(), sentinel)
		assert.True(t, math.IsInf(m.Integrated(), -1))
	})

	t.Run("delayed error", func(t *testing.T) {
		good, _ := testtools.RandomDataStreamer(2000)
		delayed := testtools.NewDelayedErrorStreamer(good, 1000, sentinel)
		m, err := loudness.NewMeter(sr, delayed)
		require.NoError(t, err)

		total := drain(t, m, 128)
		assert.Equal(t, 1000, total)
		assert.ErrorIs(t, m.Err(), sentinel)
		// Repeated reads stay drained.
		n, ok := m.Stream(make([][2]float64, 64))
		assert.Equal(t, 0, n)
		assert.False(t, ok)
	})
}

// TestMeter_NewMeterInvalidInputs checks deterministic failure for invalid
// sample rates and a nil Streamer.
func TestMeter_NewMeterInvalidInputs(t *testing.T) {
	sr := beep.SampleRate(48000)
	good := testtools.NewDataStreamer(make([][2]float64, 64))

	_, err := loudness.NewMeter(0, good)
	assert.Error(t, err)
	_, err = loudness.NewMeter(-44100, good)
	assert.Error(t, err)
	_, err = loudness.NewMeter(2000, good) // below twice the shelf frequency
	assert.Error(t, err)
	_, err = loudness.NewMeter(sr, nil)
	assert.Error(t, err)
}

// TestMeter_ReturnBehaviour adapts the core return-contract test to the
// wrapper, including reads after the stream ended.
func TestMeter_ReturnBehaviour(t *testing.T) {
	sr := beep.SampleRate(48000)
	src, _ := testtools.RandomDataStreamer(500)
	m, err := loudness.NewMeter(sr, src)
	require.NoError(t, err)

	testtools.AssertStreamerHasCorrectReturnBehaviour(t, m, 500)

	// Explicitly confirm repeated post-end reads keep returning 0, false and
	// that accessors remain callable with a finite result.
	n, ok := m.Stream(make([][2]float64, 32))
	assert.Equal(t, 0, n)
	assert.False(t, ok)
	assert.False(t, math.IsNaN(m.Integrated()))
}
