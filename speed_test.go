package beep_test

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/generators"
	"github.com/gopxl/beep/v2/internal/testtools"
)

// collectChunked drains s, requesting blocks whose sizes cycle through chunks, and returns all
// streamed samples.
func collectChunked(s beep.Streamer, chunks []int) [][2]float64 {
	var result [][2]float64
	for i := 0; ; i++ {
		buf := make([][2]float64, chunks[i%len(chunks)])
		n, ok := s.Stream(buf)
		result = append(result, buf[:n]...)
		if !ok {
			return result
		}
	}
}

// ratioAt computes the ratio for output sample i as documented on SpeedKeyframe: constant before
// the first keyframe, linearly interpolated between keyframes, and constant after the last one.
// It's an independent implementation used as the test oracle.
func ratioAt(keyframes []beep.SpeedKeyframe, i int) float64 {
	last := len(keyframes) - 1
	if i < keyframes[0].Position {
		return keyframes[0].Ratio
	}
	if i >= keyframes[last].Position {
		return keyframes[last].Ratio
	}
	for k := 0; k < last; k++ {
		k0, k1 := keyframes[k], keyframes[k+1]
		if i < k0.Position || i >= k1.Position {
			continue
		}
		frac := float64(i-k0.Position) / float64(k1.Position-k0.Position)
		return k0.Ratio + (k1.Ratio-k0.Ratio)*frac
	}
	panic("unreachable")
}

// sourcePosition returns the source position read for output sample i. Because the ratio is the
// derivative of the source position with respect to the output position, it is the sum of the
// ratios of all preceding output samples.
func sourcePosition(keyframes []beep.SpeedKeyframe, i int) float64 {
	sum := 0.0
	for j := 0; j < i; j++ {
		sum += ratioAt(keyframes, j)
	}
	return sum
}

func TestAutomateSpeed_ConstantCurveMatchesResampleRatio(t *testing.T) {
	// A single keyframe, or a curve with equal ratios everywhere, must behave like the existing
	// ResampleRatio with that ratio. This anchors the wrapper to the established Resampler.
	for _, ratio := range []float64{0.5, 1, 2.5} {
		t.Run(fmt.Sprintf("ratio_%v", ratio), func(t *testing.T) {
			_, data := testtools.RandomDataStreamer(3000)
			newSource := func() beep.Streamer {
				return testtools.NewDataStreamer(data)
			}
			keyframeSets := [][]beep.SpeedKeyframe{
				{{Position: 0, Ratio: ratio}},
				{
					{Position: 0, Ratio: ratio},
					{Position: 100, Ratio: ratio},
					{Position: 700, Ratio: ratio},
				},
			}
			for _, keyframes := range keyframeSets {
				want := testtools.Collect(beep.ResampleRatio(3, ratio, newSource()))
				a, err := beep.AutomateSpeed(3, newSource(), keyframes...)
				require.NoError(t, err)
				got := testtools.Collect(a)
				require.Equal(t, len(want), len(got))
				testtools.AssertSamplesEqual(t, want, got)
				assert.Equal(t, ratio, a.Ratio())
				assert.Equal(t, len(got), a.Position())
			}
		})
	}
}

func TestAutomateSpeed_RejectsInvalidInput(t *testing.T) {
	s, _ := testtools.RandomDataStreamer(100)

	t.Run("invalid quality", func(t *testing.T) {
		for _, quality := range []int{-1, 0, 65} {
			_, err := beep.AutomateSpeed(quality, s, beep.SpeedKeyframe{Position: 0, Ratio: 1})
			assert.Error(t, err, "quality %d", quality)
		}
	})

	t.Run("no keyframes", func(t *testing.T) {
		_, err := beep.AutomateSpeed(3, s)
		assert.Error(t, err)
	})

	t.Run("invalid ratio", func(t *testing.T) {
		for _, ratio := range []float64{0, -1, -0.5, math.Inf(1), math.Inf(-1), math.NaN()} {
			_, err := beep.AutomateSpeed(3, s, beep.SpeedKeyframe{Position: 0, Ratio: ratio})
			assert.Error(t, err, "ratio %v", ratio)
		}
	})

	t.Run("invalid ratio in later keyframe", func(t *testing.T) {
		_, err := beep.AutomateSpeed(3, s,
			beep.SpeedKeyframe{Position: 0, Ratio: 1},
			beep.SpeedKeyframe{Position: 10, Ratio: 0},
		)
		assert.Error(t, err)
	})

	t.Run("negative position", func(t *testing.T) {
		_, err := beep.AutomateSpeed(3, s, beep.SpeedKeyframe{Position: -1, Ratio: 1})
		assert.Error(t, err)
	})

	t.Run("position too large", func(t *testing.T) {
		if strconv.IntSize < 64 {
			t.Skip("sample positions above 2^53-1 don't fit in int on 32-bit platforms")
		}
		_, err := beep.AutomateSpeed(3, s, beep.SpeedKeyframe{Position: 1<<53 + 1, Ratio: 1})
		assert.Error(t, err)
	})

	t.Run("keyframes reversed", func(t *testing.T) {
		_, err := beep.AutomateSpeed(3, s,
			beep.SpeedKeyframe{Position: 100, Ratio: 1},
			beep.SpeedKeyframe{Position: 50, Ratio: 2},
		)
		assert.Error(t, err)
	})

	t.Run("duplicate positions", func(t *testing.T) {
		_, err := beep.AutomateSpeed(3, s,
			beep.SpeedKeyframe{Position: 50, Ratio: 1},
			beep.SpeedKeyframe{Position: 50, Ratio: 2},
		)
		assert.Error(t, err)
	})

	t.Run("maximum valid position", func(t *testing.T) {
		if strconv.IntSize < 64 {
			t.Skip("sample positions above 2^53-1 don't fit in int on 32-bit platforms")
		}
		a, err := beep.AutomateSpeed(3, s,
			beep.SpeedKeyframe{Position: 0, Ratio: 0.5},
			beep.SpeedKeyframe{Position: 1<<53 - 1, Ratio: 1},
		)
		require.NoError(t, err)
		assert.Equal(t, 0.5, a.Ratio())
		assert.Equal(t, 0, a.Position())
		assert.NoError(t, a.Err())
	})
}

func TestAutomateSpeed_CopiesKeyframes(t *testing.T) {
	s, _ := testtools.RandomDataStreamer(100)
	keyframes := []beep.SpeedKeyframe{
		{Position: 0, Ratio: 1},
		{Position: 10, Ratio: 2},
	}
	a, err := beep.AutomateSpeed(3, s, keyframes...)
	require.NoError(t, err)

	keyframes[0].Ratio = 100
	keyframes[1].Position = 5
	assert.Equal(t, 1.0, a.Ratio())
	testtools.CollectNum(44, a)
	assert.Equal(t, 44, a.Position())
}

func TestAutomateSpeed_CurveStartsAndEndsConstant(t *testing.T) {
	s, _ := testtools.RandomDataStreamer(1000)
	keyframes := []beep.SpeedKeyframe{
		{Position: 100, Ratio: 0.5},
		{Position: 200, Ratio: 1.5},
	}
	a, err := beep.AutomateSpeed(3, s, keyframes...)
	require.NoError(t, err)

	// Before the first keyframe the first ratio is used.
	assert.Equal(t, 0.5, a.Ratio())
	testtools.CollectNum(50, a)
	assert.Equal(t, 50, a.Position())
	assert.Equal(t, 0.5, a.Ratio())

	// Mid-ramp the ratio is interpolated.
	testtools.CollectNum(100, a) // position 150, halfway from 100 to 200
	assert.Equal(t, 150, a.Position())
	assert.InDelta(t, 1.0, a.Ratio(), 1e-12)

	// After the last keyframe the last ratio is stable forever.
	testtools.CollectNum(100, a) // position 250, past the last keyframe
	assert.Equal(t, 1.5, a.Ratio())
	for i := 0; i < 5; i++ {
		testtools.CollectNum(100, a)
		assert.Equal(t, 1.5, a.Ratio())
	}
}

func TestAutomateSpeed_OutputIndependentOfChunkSize(t *testing.T) {
	const (
		sampleRate = beep.SampleRate(8000)
		numSamples = 4000
	)
	keyframes := []beep.SpeedKeyframe{
		{Position: 0, Ratio: 0.25},
		{Position: sampleRate.N(200 * time.Millisecond), Ratio: 0.5},
		{Position: sampleRate.N(400 * time.Millisecond), Ratio: 2},
		{Position: sampleRate.N(480 * time.Millisecond), Ratio: 1},
	}

	newAutomator := func(t *testing.T) *beep.SpeedAutomator {
		t.Helper()
		source, _ := testtools.NewSequentialDataStreamer(numSamples)
		a, err := beep.AutomateSpeed(3, source, keyframes...)
		require.NoError(t, err)
		return a
	}

	want := testtools.Collect(newAutomator(t))
	require.NotEmpty(t, want)

	chunkings := [][]int{
		{1},
		{2},
		{7},
		{64},
		{513},
		{1, 2, 3, 5, 8, 13, 21},
		{1000, 1, 512, 3},
	}
	for _, chunks := range chunkings {
		chunks := chunks
		name := "chunks_" + joinInts(chunks)
		t.Run(name, func(t *testing.T) {
			got := collectChunked(newAutomator(t), chunks)
			testtools.AssertSamplesEqual(t, want, got)
			assert.Len(t, got, len(want))
		})
	}
}

func joinInts(ns []int) string {
	ss := make([]string, len(ns))
	for i, n := range ns {
		ss[i] = fmt.Sprint(n)
	}
	return strings.Join(ss, "_")
}

type lagrangePoint struct{ X, Y float64 }

func lagrangeFloat(pts []lagrangePoint, x float64) (y float64) {
	for j := range pts {
		l := 1.0
		for m := range pts {
			if j == m {
				continue
			}
			l *= (x - pts[m].X) / (pts[j].X - pts[m].X)
		}
		y += pts[j].Y * l
	}
	return y
}

// TestAutomateSpeed_TracksSpeedCurveWithSine checks the actual speed trajectory. A sine tone
// resampled with ratio ρ(i) is read at output sample i from source position Σ_{j<i} ρ(j), so the
// output is an independently computable interpolation of the source at those exact positions.
// Comparing samples (rather than estimating instantaneous frequency) catches a wrong trajectory,
// an off-by-one source position, or an interpolation bug.
func TestAutomateSpeed_TracksSpeedCurveWithSine(t *testing.T) {
	const (
		sampleRate = beep.SampleRate(8000)
		freq       = 40.0
		numSamples = 3000
		quality    = 3
	)
	newSine := func() beep.Streamer {
		s, err := generators.SineTone(sampleRate, freq)
		require.NoError(t, err)
		return beep.Take(numSamples, s)
	}
	data := testtools.Collect(newSine())

	keyframes := []beep.SpeedKeyframe{
		{Position: 0, Ratio: 0.5},
		{Position: 100, Ratio: 0.8},
		{Position: 350, Ratio: 1.4},
		{Position: 700, Ratio: 1},
	}

	a, err := beep.AutomateSpeed(quality, newSine(), keyframes...)
	require.NoError(t, err)
	got := testtools.Collect(a)

	// Compute the expected output directly from the source data and the keyframe curve, clamping
	// the interpolation window to the available source samples exactly like Resampler does.
	var want [][2]float64
	for i := 0; ; i++ {
		pos := sourcePosition(keyframes, i)
		if int(pos) >= len(data) {
			break
		}
		var sample [2]float64
		for c := range sample {
			var pts []lagrangePoint
			for k := -(quality*2 - 1) / 2; k < quality*2-(quality*2-1)/2; k++ {
				x := int(pos) + k
				if x < 0 || x >= len(data) {
					continue
				}
				pts = append(pts, lagrangePoint{float64(x), data[x][c]})
			}
			sample[c] = lagrangeFloat(pts, pos)
		}
		want = append(want, sample)
	}

	require.Equal(t, len(want), len(got))
	testtools.AssertSamplesEqual(t, want, got)

	assert.Equal(t, len(got), a.Position())
	assert.NoError(t, a.Err())
}

func TestAutomateSpeed_TerminatesWhenSourceDrains(t *testing.T) {
	const sourceLen = 500
	// The curve ends far beyond the source, so the source drains first.
	keyframes := []beep.SpeedKeyframe{
		{Position: 0, Ratio: 1},
		{Position: 100000, Ratio: 2},
	}

	t.Run("collect", func(t *testing.T) {
		source, _ := testtools.NewSequentialDataStreamer(sourceLen)
		a, err := beep.AutomateSpeed(3, source, keyframes...)
		require.NoError(t, err)
		got := testtools.Collect(a)
		require.NotEmpty(t, got)
		assert.Equal(t, len(got), a.Position())
		assert.Less(t, a.Position(), keyframes[len(keyframes)-1].Position)
		assert.NoError(t, a.Err())

		// Further calls stay drained.
		n, ok := a.Stream(make([][2]float64, 10))
		assert.Equal(t, 0, n)
		assert.False(t, ok)
	})

	t.Run("correct return behaviour", func(t *testing.T) {
		newAutomator := func() *beep.SpeedAutomator {
			source, _ := testtools.NewSequentialDataStreamer(sourceLen)
			a, err := beep.AutomateSpeed(3, source, keyframes...)
			require.NoError(t, err)
			return a
		}

		// The drifting ratio means the automator yields slightly fewer than sourceLen output
		// samples; determine the exact count with a separate instance so the return-value
		// contract can be checked.
		expected := len(testtools.Collect(newAutomator()))
		require.Greater(t, expected, 51)

		// All requested samples are produced before the source drains, then a partial block, then
		// (0, false) forever.
		testtools.AssertStreamerHasCorrectReturnBehaviour(t, newAutomator(), expected)
	})
}

func TestAutomateSpeed_CurveEndDoesNotDrain(t *testing.T) {
	// Source longer than the curve: the automator continues with the last ratio instead of
	// stopping at the end of the curve.
	source, _ := testtools.NewSequentialDataStreamer(2000)
	a, err := beep.AutomateSpeed(3, source,
		beep.SpeedKeyframe{Position: 0, Ratio: 0.5},
		beep.SpeedKeyframe{Position: 100, Ratio: 1},
	)
	require.NoError(t, err)
	got := testtools.CollectNum(400, a)
	assert.Len(t, got, 400)
	assert.Equal(t, 400, a.Position())
	assert.Equal(t, 1.0, a.Ratio())
	for i := 1; i < len(got); i++ {
		assert.Greater(t, got[i][0], got[i-1][0], "sequential source must keep advancing")
	}
}

func TestAutomateSpeed_PropagatesErrors(t *testing.T) {
	t.Run("delayed error", func(t *testing.T) {
		wantErr := errors.New("boom")
		inner, _ := testtools.NewSequentialDataStreamer(10000)
		source := testtools.NewDelayedErrorStreamer(inner, 500, wantErr)

		a, err := beep.AutomateSpeed(3, source, beep.SpeedKeyframe{Position: 0, Ratio: 1})
		require.NoError(t, err)

		got := testtools.Collect(a)
		assert.NotEmpty(t, got)
		assert.ErrorIs(t, a.Err(), wantErr)
		assert.Equal(t, len(got), a.Position())
	})

	t.Run("immediate error", func(t *testing.T) {
		wantErr := errors.New("immediate")
		source := testtools.NewErrorStreamer(wantErr)

		a, err := beep.AutomateSpeed(3, source, beep.SpeedKeyframe{Position: 0, Ratio: 1})
		require.NoError(t, err)
		assert.ErrorIs(t, a.Err(), wantErr)

		n, ok := a.Stream(make([][2]float64, 32))
		assert.Equal(t, 0, n)
		assert.False(t, ok)
		assert.ErrorIs(t, a.Err(), wantErr)
		assert.Equal(t, 0, a.Position())
	})
}

func TestAutomateSpeed_PreservesStereoOrder(t *testing.T) {
	// Distinct left/right values make channel swaps or averaging obvious. At ratio 1 a single
	// source sample is read per output sample, so values stay identically distinct.
	data := make([][2]float64, 200)
	for i := range data {
		data[i] = [2]float64{0.25 + float64(i), -0.25 - float64(i)}
	}
	source := testtools.NewDataStreamer(data)
	a, err := beep.AutomateSpeed(3, source,
		beep.SpeedKeyframe{Position: 0, Ratio: 1},
		beep.SpeedKeyframe{Position: 1000, Ratio: 1},
	)
	require.NoError(t, err)
	got := testtools.Collect(a)
	require.Len(t, got, len(data))
	for i := range got {
		assert.InDelta(t, data[i][0], got[i][0], 1e-9)
		assert.InDelta(t, data[i][1], got[i][1], 1e-9)
	}
	assert.Greater(t, got[0][0], 0.0)
	assert.Less(t, got[0][1], 0.0)
}

func TestAutomateSpeed_RatioAtBoundaries(t *testing.T) {
	s, _ := testtools.RandomDataStreamer(100)
	keyframes := []beep.SpeedKeyframe{
		{Position: 10, Ratio: 0.5},
		{Position: 20, Ratio: 1.5},
		{Position: 30, Ratio: 2.5},
	}
	a, err := beep.AutomateSpeed(3, s, keyframes...)
	require.NoError(t, err)

	// The sample just before a boundary uses the end of the previous segment.
	testtools.CollectNum(19, a)
	assert.Equal(t, 19, a.Position())
	assert.InDelta(t, ratioAt(keyframes, 19), a.Ratio(), 1e-12)

	// The boundary sample itself belongs to the next segment and uses its start ratio exactly.
	testtools.CollectNum(1, a)
	assert.Equal(t, 20, a.Position())
	assert.InDelta(t, 1.5, a.Ratio(), 1e-12)

	// The last keyframe's ratio is held after the end of the curve.
	testtools.CollectNum(10, a)
	assert.Equal(t, 30, a.Position())
	assert.InDelta(t, 2.5, a.Ratio(), 1e-12)
}

// ExampleAutomateSpeed shows the motivating case: ease a tone from half speed to normal speed over
// one second of output, with the ratio updated per output sample.
func ExampleAutomateSpeed() {
	sampleRate := beep.SampleRate(44100)
	s, err := generators.SineTone(sampleRate, 440)
	if err != nil {
		panic(err)
	}

	automator, err := beep.AutomateSpeed(4, s,
		beep.SpeedKeyframe{Position: 0, Ratio: 0.5},
		beep.SpeedKeyframe{Position: sampleRate.N(time.Second), Ratio: 1},
	)
	if err != nil {
		panic(err)
	}

	fmt.Printf("start:   %.2fx\n", automator.Ratio())

	// The ratio at output position p depends only on p, so block size doesn't matter.
	testtools.CollectNum(sampleRate.N(time.Second/2), automator)
	fmt.Printf("halfway: %.2fx\n", automator.Ratio())

	// Output:
	// start:   0.50x
	// halfway: 0.75x
}
