package effects_test

import (
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/effects"
	"github.com/gopxl/beep/v2/internal/testtools"
)

// referenceReverb computes the expected reverb output in the time domain:
// out = (1-wet)*input + wet*(input * impulseResponse), truncated to len(input)
// samples, or extended by len(impulseResponse)-1 tail samples when tail is on.
func referenceReverb(input, impulseResponse [][2]float64, wet float64, tail bool) [][2]float64 {
	length := len(input)
	if tail {
		length += len(impulseResponse) - 1
	}
	out := make([][2]float64, length)
	for i := range out {
		var wetL, wetR float64
		for j := range impulseResponse {
			if k := i - j; k >= 0 && k < len(input) {
				wetL += input[k][0] * impulseResponse[j][0]
				wetR += input[k][1] * impulseResponse[j][1]
			}
		}
		var dryL, dryR float64
		if i < len(input) {
			dryL, dryR = input[i][0], input[i][1]
		}
		out[i] = [2]float64{(1-wet)*dryL + wet*wetL, (1-wet)*dryR + wet*wetR}
	}
	return out
}

// collectChunked drains s, requesting chunk samples at a time.
func collectChunked(s beep.Streamer, chunk int) [][2]float64 {
	var result [][2]float64
	buf := make([][2]float64, chunk)
	for {
		n, ok := s.Stream(buf)
		if !ok {
			return result
		}
		result = append(result, buf[:n]...)
	}
}

func TestReverbUnitImpulse(t *testing.T) {
	// A unit impulse impulse response must leave the signal unchanged.
	s, data := testtools.RandomDataStreamer(5000)
	r, err := effects.NewReverb(s, [][2]float64{{1, 1}}, 1)
	assert.NoError(t, err)

	got := testtools.Collect(r)
	testtools.AssertSamplesEqual(t, data, got)
}

func TestReverbTwoTapImpulseResponse(t *testing.T) {
	// h = [1, 0.5]: out[i] = in[i] + 0.5*in[i-1], with one tail sample.
	s, data := testtools.RandomDataStreamer(3000)
	ir := [][2]float64{{1, 1}, {0.5, 0.5}}
	r, err := effects.NewReverb(s, ir, 1, effects.WithReverbTail(true))
	assert.NoError(t, err)

	got := testtools.Collect(r)
	want := referenceReverb(data, ir, 1, true)
	assert.Equal(t, len(data)+len(ir)-1, len(got))
	testtools.AssertSamplesEqual(t, want, got)
}

func TestReverbStereoImpulseResponse(t *testing.T) {
	// Different impulse responses per channel, crossing multiple partitions.
	s, data := testtools.RandomDataStreamer(5000)
	ir := make([][2]float64, 2500)
	for i := range ir {
		ir[i] = [2]float64{
			math.Exp(-float64(i)/500) * math.Sin(float64(i)/7),
			math.Exp(-float64(i)/700) * math.Cos(float64(i)/11),
		}
	}
	r, err := effects.NewReverb(s, ir, 0.8, effects.WithReverbTail(true))
	assert.NoError(t, err)

	got := testtools.Collect(r)
	want := referenceReverb(data, ir, 0.8, true)
	assert.Equal(t, len(data)+len(ir)-1, len(got))
	testtools.AssertSamplesEqual(t, want, got)
}

func TestReverbMonoImpulseResponse(t *testing.T) {
	s, data := testtools.RandomDataStreamer(3000)
	ir := []float64{0.6, -0.3, 0.15, -0.05}
	r, err := effects.NewMonoReverb(s, ir, 1, effects.WithReverbTail(true))
	assert.NoError(t, err)

	stereoIR := make([][2]float64, len(ir))
	for i, v := range ir {
		stereoIR[i] = [2]float64{v, v}
	}
	want := referenceReverb(data, stereoIR, 1, true)

	got := testtools.Collect(r)
	testtools.AssertSamplesEqual(t, want, got)
}

func TestReverbOutputIndependentOfChunkSize(t *testing.T) {
	ir := [][2]float64{{1, 0.5}, {0.5, 0.25}, {-0.25, 0.125}, {0.125, -0.0625}}
	_, data := testtools.RandomDataStreamer(5000)
	want := referenceReverb(data, ir, 0.7, true)
	for _, chunk := range []int{1, 3, 479, 1024, 1025, 4096} {
		r, err := effects.NewReverb(testtools.NewDataStreamer(data), ir, 0.7, effects.WithReverbTail(true))
		assert.NoError(t, err)
		got := collectChunked(r, chunk)
		testtools.AssertSamplesEqual(t, want, got)
	}
}

func TestReverbTail(t *testing.T) {
	ir := [][2]float64{{1, 1}, {0.5, 0.5}, {0.25, 0.25}}

	// Without the tail option the reverb drains together with its source.
	s, data := testtools.RandomDataStreamer(2000)
	r, err := effects.NewReverb(s, ir, 1)
	assert.NoError(t, err)
	got := testtools.Collect(r)
	assert.Equal(t, len(data), len(got))
	testtools.AssertSamplesEqual(t, referenceReverb(data, ir, 1, false), got)

	// With the tail option the full convolution, including its decay, is streamed.
	s, data = testtools.RandomDataStreamer(2000)
	r, err = effects.NewReverb(s, ir, 1, effects.WithReverbTail(true))
	assert.NoError(t, err)
	got = testtools.Collect(r)
	assert.Equal(t, len(data)+len(ir)-1, len(got))
	testtools.AssertSamplesEqual(t, referenceReverb(data, ir, 1, true), got)
}

func TestReverbWetDryMix(t *testing.T) {
	ir := [][2]float64{{0.5, 0.25}, {0.25, 0.5}}

	// Fully dry: the signal passes through unchanged.
	s, data := testtools.RandomDataStreamer(2000)
	r, err := effects.NewReverb(s, ir, 0)
	assert.NoError(t, err)
	testtools.AssertSamplesEqual(t, data, testtools.Collect(r))

	// A 0.25 mix: 0.75*dry + 0.25*convolution.
	s, data = testtools.RandomDataStreamer(2000)
	r, err = effects.NewReverb(s, ir, 0.25)
	assert.NoError(t, err)
	testtools.AssertSamplesEqual(t, referenceReverb(data, ir, 0.25, false), testtools.Collect(r))
}

func TestReverbSeekResetsState(t *testing.T) {
	ir := [][2]float64{{1, 1}, {0.5, 0.5}, {0.25, 0.25}}

	s, data := testtools.RandomDataStreamer(3000)
	r, err := effects.NewReverb(s, ir, 1, effects.WithReverbTail(true))
	assert.NoError(t, err)

	// Stream a part of the signal to build up convolution state.
	collectChunked(r, 1000)

	// After seeking back, the output must equal a fresh reverb's output.
	assert.NoError(t, r.Seek(0))
	fresh, err := effects.NewReverb(testtools.NewDataStreamer(data), ir, 1, effects.WithReverbTail(true))
	assert.NoError(t, err)
	testtools.AssertSamplesEqual(t, testtools.Collect(fresh), testtools.Collect(r))

	// Seeking to the middle must behave like a fresh reverb over the remainder.
	assert.NoError(t, r.Seek(500))
	fresh, err = effects.NewReverb(testtools.NewDataStreamer(data[500:]), ir, 1, effects.WithReverbTail(true))
	assert.NoError(t, err)
	testtools.AssertSamplesEqual(t, testtools.Collect(fresh), testtools.Collect(r))
}

func TestReverbSeekNonSeekableSource(t *testing.T) {
	r, err := effects.NewReverb(beep.Silence(-1), [][2]float64{{1, 1}}, 1)
	assert.NoError(t, err)
	assert.Error(t, r.Seek(0))
}

func TestReverbErrorPropagation(t *testing.T) {
	errWant := errors.New("test error")
	s := testtools.NewDelayedErrorStreamer(testtools.NewDataStreamer(make([][2]float64, 1500)), 1500, errWant)
	r, err := effects.NewReverb(s, [][2]float64{{1, 1}, {0.5, 0.5}}, 1, effects.WithReverbTail(true))
	assert.NoError(t, err)

	// The reverb streams until the source errors, then becomes drained.
	testtools.Collect(r)
	assert.Equal(t, errWant, r.Err())

	// After the error the reverb stays drained; no tail is produced.
	n, ok := r.Stream(make([][2]float64, 512))
	assert.False(t, ok)
	assert.Equal(t, 0, n)
	assert.Equal(t, errWant, r.Err())
}

func TestReverbReturnBehaviour(t *testing.T) {
	s, _ := testtools.RandomDataStreamer(5000)
	r, err := effects.NewReverb(s, [][2]float64{{1, 1}, {0.5, 0.5}}, 0.5)
	assert.NoError(t, err)
	testtools.AssertStreamerHasCorrectReturnBehaviour(t, r, 5000)
}

func TestReverbValidation(t *testing.T) {
	s := testtools.NewDataStreamer(make([][2]float64, 10))

	t.Run("empty impulse response", func(t *testing.T) {
		_, err := effects.NewReverb(s, nil, 0.5)
		assert.ErrorIs(t, err, effects.ErrEmptyImpulseResponse)
		_, err = effects.NewMonoReverb(s, nil, 0.5)
		assert.ErrorIs(t, err, effects.ErrEmptyImpulseResponse)
	})

	t.Run("too long impulse response", func(t *testing.T) {
		ir := make([][2]float64, 5)
		_, err := effects.NewReverb(s, ir, 0.5, effects.WithReverbMaxIRLength(4))
		assert.ErrorIs(t, err, effects.ErrImpulseResponseTooLong)
		_, err = effects.NewMonoReverb(s, make([]float64, 5), 0.5, effects.WithReverbMaxIRLength(4))
		assert.ErrorIs(t, err, effects.ErrImpulseResponseTooLong)
	})

	t.Run("non-finite impulse response", func(t *testing.T) {
		for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
			_, err := effects.NewReverb(s, [][2]float64{{1, 1}, {bad, 0}}, 0.5)
			assert.ErrorIs(t, err, effects.ErrNonFiniteImpulseResponse)
			_, err = effects.NewReverb(s, [][2]float64{{1, 1}, {0, bad}}, 0.5)
			assert.ErrorIs(t, err, effects.ErrNonFiniteImpulseResponse)
			_, err = effects.NewMonoReverb(s, []float64{1, bad}, 0.5)
			assert.ErrorIs(t, err, effects.ErrNonFiniteImpulseResponse)
		}
	})

	t.Run("invalid wet/dry ratio", func(t *testing.T) {
		ir := [][2]float64{{1, 1}}
		for _, bad := range []float64{-0.1, 1.1, math.NaN(), math.Inf(1), math.Inf(-1)} {
			_, err := effects.NewReverb(s, ir, bad)
			assert.ErrorIs(t, err, effects.ErrInvalidWetDryRatio)
			_, err = effects.NewMonoReverb(s, []float64{1}, bad)
			assert.ErrorIs(t, err, effects.ErrInvalidWetDryRatio)
		}
	})

	t.Run("invalid max impulse response length", func(t *testing.T) {
		_, err := effects.NewReverb(s, [][2]float64{{1, 1}}, 0.5, effects.WithReverbMaxIRLength(0))
		assert.ErrorIs(t, err, effects.ErrInvalidMaxIRLength)
	})
}
