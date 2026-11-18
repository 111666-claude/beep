package beep_test

import (
	"errors"
	"math/rand"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/internal/testtools"
)

func TestTake(t *testing.T) {
	for i := 0; i < 7; i++ {
		total := rand.Intn(1e5) + 1e4
		s, data := testtools.RandomDataStreamer(total)
		take := rand.Intn(total)

		want := data[:take]
		got := testtools.Collect(beep.Take(take, s))

		if !reflect.DeepEqual(want, got) {
			t.Error("Take not working correctly")
		}
	}
}

func TestLoop(t *testing.T) {
	for i := 0; i < 7; i++ {
		for n := 0; n < 5; n++ {
			s, data := testtools.RandomDataStreamer(10)

			var want [][2]float64
			for j := 0; j < n; j++ {
				want = append(want, data...)
			}
			got := testtools.Collect(beep.Loop(n, s))

			if !reflect.DeepEqual(want, got) {
				t.Error("Loop not working correctly")
			}
		}
	}
}

func TestLoop2(t *testing.T) {
	// LoopStart is bigger than s.Len()
	s, _ := testtools.NewSequentialDataStreamer(5)
	l, err := beep.Loop2(s, beep.LoopStart(5))
	assert.EqualError(t, err, "invalid argument to Loop2; start position 5 must be smaller than the source streamer length 5")

	// LoopStart is bigger than LoopEnd
	s, _ = testtools.NewSequentialDataStreamer(5)
	l, err = beep.Loop2(s, beep.LoopBetween(4, 4))
	assert.EqualError(t, err, "invalid argument to Loop2; start position 4 must be smaller than the end position 4")

	// Loop indefinitely (no options).
	s, _ = testtools.NewSequentialDataStreamer(5)
	l, err = beep.Loop2(s)
	assert.NoError(t, err)
	got := testtools.CollectNum(16, l)
	assert.Equal(t, [][2]float64{{0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}, {0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}, {0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}, {0, 0}}, got)

	// Test no loop.
	s, _ = testtools.NewSequentialDataStreamer(5)
	l, err = beep.Loop2(s, beep.LoopTimes(0))
	assert.NoError(t, err)
	got = testtools.Collect(l)
	assert.Equal(t, [][2]float64{{0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}}, got)

	// Test loop once.
	s, _ = testtools.NewSequentialDataStreamer(5)
	l, err = beep.Loop2(s, beep.LoopTimes(1))
	assert.NoError(t, err)
	got = testtools.Collect(l)
	assert.Equal(t, [][2]float64{{0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}, {0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}}, got)

	// Test loop twice.
	s, _ = testtools.NewSequentialDataStreamer(5)
	l, err = beep.Loop2(s, beep.LoopTimes(2))
	assert.NoError(t, err)
	got = testtools.Collect(l)
	assert.Equal(t, [][2]float64{{0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}, {0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}, {0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}}, got)

	// Test loop from start position.
	s, _ = testtools.NewSequentialDataStreamer(5)
	l, err = beep.Loop2(s, beep.LoopTimes(2), beep.LoopStart(2))
	assert.NoError(t, err)
	got = testtools.Collect(l)
	assert.Equal(t, [][2]float64{{0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}, {2, 2}, {3, 3}, {4, 4}, {2, 2}, {3, 3}, {4, 4}}, got)

	// Test loop with end position.
	s, _ = testtools.NewSequentialDataStreamer(5)
	l, err = beep.Loop2(s, beep.LoopTimes(2), beep.LoopEnd(4))
	assert.NoError(t, err)
	got = testtools.Collect(l)
	assert.Equal(t, [][2]float64{{0, 0}, {1, 1}, {2, 2}, {3, 3}, {0, 0}, {1, 1}, {2, 2}, {3, 3}, {0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}}, got)

	// Test loop with start and end position.
	s, _ = testtools.NewSequentialDataStreamer(5)
	l, err = beep.Loop2(s, beep.LoopTimes(2), beep.LoopBetween(2, 4))
	assert.NoError(t, err)
	got = testtools.Collect(l)
	assert.Equal(t, [][2]float64{{0, 0}, {1, 1}, {2, 2}, {3, 3}, {2, 2}, {3, 3}, {2, 2}, {3, 3}, {4, 4}}, got)

	// Loop indefinitely with both start and end position.
	s, _ = testtools.NewSequentialDataStreamer(5)
	l, err = beep.Loop2(s, beep.LoopBetween(2, 4))
	assert.NoError(t, err)
	got = testtools.CollectNum(10, l)
	assert.Equal(t, [][2]float64{{0, 0}, {1, 1}, {2, 2}, {3, 3}, {2, 2}, {3, 3}, {2, 2}, {3, 3}, {2, 2}, {3, 3}}, got)

	//// Test streaming from the middle of the loops.
	s, _ = testtools.NewSequentialDataStreamer(5)
	l, err = beep.Loop2(s, beep.LoopTimes(2), beep.LoopBetween(2, 4)) // 0, 1, 2, 3, 2, 3, 2, 3
	assert.NoError(t, err)
	// First stream to the middle of a loop.
	buf := make([][2]float64, 3)
	if n, ok := l.Stream(buf); n != 3 || !ok {
		t.Fatalf("want n %d got %d, want ok %t got %t", 3, n, true, ok)
	}
	assert.Equal(t, [][2]float64{{0, 0}, {1, 1}, {2, 2}}, buf)
	// Then stream starting at the middle of the loop.
	if n, ok := l.Stream(buf); n != 3 || !ok {
		t.Fatalf("want n %d got %d, want ok %t got %t", 3, n, true, ok)
	}
	assert.Equal(t, [][2]float64{{3, 3}, {2, 2}, {3, 3}}, buf)

	// Test error handling in middle of loop.
	expectedErr := errors.New("expected error")
	s, _ = testtools.NewSequentialDataStreamer(5)
	s = testtools.NewDelayedErrorStreamer(s, 5, expectedErr)
	l, err = beep.Loop2(s, beep.LoopTimes(3), beep.LoopBetween(2, 4)) // 0, 1, 2, 3, 2, 3, 2, 3
	assert.NoError(t, err)
	buf = make([][2]float64, 10)
	if n, ok := l.Stream(buf); n != 5 || !ok {
		t.Fatalf("want n %d got %d, want ok %t got %t", 5, n, true, ok)
	}
	assert.Equal(t, [][2]float64{{0, 0}, {1, 1}, {2, 2}, {3, 3}, {2, 2}, {0, 0}, {0, 0}, {0, 0}, {0, 0}, {0, 0}}, buf)
	assert.Equal(t, expectedErr, l.Err())
	if n, ok := l.Stream(buf); n != 0 || ok {
		t.Fatalf("want n %d got %d, want ok %t got %t", 0, n, false, ok)
	}
	assert.Equal(t, expectedErr, l.Err())

	// Test error handling during call to Seek().
	s, _ = testtools.NewSequentialDataStreamer(5)
	s = testtools.NewSeekErrorStreamer(s, expectedErr)
	l, err = beep.Loop2(s, beep.LoopTimes(3), beep.LoopBetween(2, 4)) // 0, 1, 2, 3, [error]
	assert.NoError(t, err)
	buf = make([][2]float64, 10)
	if n, ok := l.Stream(buf); n != 4 || !ok {
		t.Fatalf("want n %d got %d, want ok %t got %t", 4, n, true, ok)
	}
	assert.Equal(t, [][2]float64{{0, 0}, {1, 1}, {2, 2}, {3, 3}, {0, 0}, {0, 0}, {0, 0}, {0, 0}, {0, 0}, {0, 0}}, buf)
	assert.Equal(t, expectedErr, l.Err())
	if n, ok := l.Stream(buf); n != 0 || ok {
		t.Fatalf("want n %d got %d, want ok %t got %t", 0, n, false, ok)
	}
	assert.Equal(t, expectedErr, l.Err())
}

// constantStreamer returns a streamer whose samples all have the same value v
// on both channels: a constant-amplitude waveform.
func constantStreamer(numSamples int, v float64) beep.StreamSeeker {
	data := make([][2]float64, numSamples)
	for i := range data {
		data[i] = [2]float64{v, v}
	}
	return testtools.NewDataStreamer(data)
}

// stepStreamer returns a streamer with two constant-amplitude sections: value
// high up to (and including) index step-1, value low afterwards: a single
// step/jump waveform.
func stepStreamer(numSamples int, step int, high, low float64) beep.StreamSeeker {
	data := make([][2]float64, numSamples)
	for i := range data {
		v := high
		if i >= step {
			v = low
		}
		data[i] = [2]float64{v, v}
	}
	return testtools.NewDataStreamer(data)
}

// collectChunked drains s using buffers of the given sizes and returns all
// streamed samples, allowing tests to prove the output is independent of the
// Stream buffer (chunk) size.
func collectChunked(s beep.Streamer, chunkSizes ...int) [][2]float64 {
	var result [][2]float64
	for i := 0; ; i++ {
		buf := make([][2]float64, chunkSizes[i%len(chunkSizes)])
		n, ok := s.Stream(buf)
		result = append(result, buf[:n]...)
		if !ok {
			return result
		}
	}
}

// referenceLoop2Clean is an independent, straightforward reimplementation of
// the Loop2 output semantics (including cross-fading), used to cross-check the
// actual implementation. The non-overlapping parameter combinations it is
// checked against need only a single xfade-long head buffer.
func referenceLoop2Clean(data [][2]float64, start, end, times, xfade int) [][2]float64 {
	// times == 0: the source is played once, unchanged.
	if times == 0 {
		return append([][2]float64(nil), data...)
	}
	var out [][2]float64
	gain := func(k int) float64 {
		if xfade == 1 {
			return 1
		}
		return float64(k) / float64(xfade-1)
	}
	emit := func(from int, blend bool) {
		for p := from; p < end; p++ {
			if blend && xfade > 0 && p >= end-xfade {
				k := p - (end - xfade)
				g := gain(k)
				out = append(out, [2]float64{
					data[p][0]*(1-g) + data[start+k][0]*g,
					data[p][1]*(1-g) + data[start+k][1]*g,
				})
			} else {
				out = append(out, data[p])
			}
		}
	}

	out = append(out, data[:start]...)
	if times < 0 {
		// Infinite loop: only a finite prefix is ever collected.
		for r := 0; r < 4; r++ {
			from := start
			if r > 0 {
				from = start + xfade
			}
			emit(from, true)
		}
		return out
	}
	// Finite: `times` blended repetitions, each followed by another repetition,
	// then one final unblended repetition and the tail after the section.
	for r := 0; r < times; r++ {
		from := start
		if r > 0 {
			from = start + xfade
		}
		emit(from, true)
	}
	emit(start+xfade, false)
	out = append(out, data[end:]...)
	return out
}

func TestLoop2Crossfade(t *testing.T) {
	// Construction-time validation: the cross-fade must fit inside the section.
	s := constantStreamer(5, 1)
	_, err := beep.Loop2(s, beep.LoopCrossfade(5))
	assert.EqualError(t, err, "invalid argument to Loop2; crossfade length 5 must be smaller than the loop section length 5")
	s = constantStreamer(5, 1)
	_, err = beep.Loop2(s, beep.LoopBetween(1, 4), beep.LoopCrossfade(3))
	assert.EqualError(t, err, "invalid argument to Loop2; crossfade length 3 must be smaller than the loop section length 3")
	assert.Panics(t, func() { beep.LoopCrossfade(-1) })

	// A length of 0 is equivalent to not setting the option: default loop
	// behavior is unchanged.
	s = constantStreamer(4, 1)
	l, err := beep.Loop2(s, beep.LoopTimes(1), beep.LoopCrossfade(0))
	assert.NoError(t, err)
	assert.Equal(t, testtools.Collect(constantStreamer(8, 1)), testtools.Collect(l))

	// Constant-amplitude waveform: every seam frame stays at the same level, so
	// the entire output must be bit-for-bit identical to a plain stream of the
	// same (cross-fade dependent) length. This proves the seams are click-free
	// and gain-preserving.
	for _, xfade := range []int{1, 2, 3} {
		s = constantStreamer(5, 0.75)
		l, err = beep.Loop2(s, beep.LoopTimes(3), beep.LoopCrossfade(xfade))
		assert.NoError(t, err)
		totalLen := 5*4 - 3*xfade // plain loop length (4 repetitions) minus one blended head per seam
		plain := testtools.Collect(constantStreamer(totalLen, 0.75))
		assert.Equal(t, plain, testtools.Collect(l), "constant amplitude with xfade %d", xfade)
		assert.Equal(t, 20, len(testtools.Collect(mustLoop2(t, constantStreamer(5, 0.75), beep.LoopTimes(3)))))
	}

	// Step waveform with a full-stream loop, xfade = 3. Gains across the zone
	// are k/(xfade-1) = 0, 0.5, 1: the tail fades out while the head fades in,
	// replacing the plain loop's hard 0 -> 1 jump at the seam with a smooth
	// 0 -> 0.5 -> 1 ramp.
	s = stepStreamer(6, 3, 1, 0)
	l, err = beep.Loop2(s, beep.LoopTimes(2), beep.LoopCrossfade(3))
	assert.NoError(t, err)
	want := [][2]float64{
		{1, 1}, {1, 1}, {1, 1}, // unity region
		{0, 0}, {0.5, 0.5}, {1, 1}, // first blended repetition
		{0, 0}, {0.5, 0.5}, {1, 1}, // second blended repetition, from start+3
		{0, 0}, {0, 0}, {0, 0}, // final, unblended repetition, from start+3
	}
	assert.Equal(t, want, testtools.Collect(l))

	// Without cross-fading the same source jumps straight from 0 back to 1 at
	// every seam.
	s = stepStreamer(5, 3, 1, 0)
	l, err = beep.Loop2(s, beep.LoopTimes(2))
	assert.NoError(t, err)
	assert.Equal(t, [][2]float64{
		{1, 1}, {1, 1}, {1, 1}, {0, 0}, {0, 0},
		{1, 1}, {1, 1}, {1, 1}, {0, 0}, {0, 0},
		{1, 1}, {1, 1}, {1, 1}, {0, 0}, {0, 0},
	}, testtools.Collect(l))

	// xfade = 1: the single seam frame is fully replaced by the head sample,
	// and every following repetition starts at start+1.
	s = stepStreamer(4, 2, 1, 0)
	l, err = beep.Loop2(s, beep.LoopTimes(1), beep.LoopCrossfade(1))
	assert.NoError(t, err)
	assert.Equal(t, [][2]float64{
		{1, 1}, {1, 1}, {0, 0}, {1, 1}, // blended repetition: seam frame replaced by head data[0]
		{1, 1}, {0, 0}, {0, 0}, // final, unblended repetition from start+1
	}, testtools.Collect(l))

	// Deterministic length and chunk independence: the blended head replaces
	// the replayed head at every seam, so the cross-faded output is exactly
	// times*xfade samples shorter than the plain loop, and is identical for
	// every Stream buffer (chunk) size.
	s, data := testtools.NewSequentialDataStreamer(8)
	l, err = beep.Loop2(s, beep.LoopTimes(2), beep.LoopBetween(2, 7), beep.LoopCrossfade(2))
	assert.NoError(t, err)
	plainLen := len(testtools.Collect(mustLoop2(t, testtools.NewDataStreamer(data), beep.LoopTimes(2), beep.LoopBetween(2, 7))))
	chunks := collectChunked(l, 1, 3, 7, 2, 5)
	assert.Equal(t, plainLen-2*2, len(chunks))
	s, _ = testtools.NewSequentialDataStreamer(8)
	l, err = beep.Loop2(s, beep.LoopTimes(2), beep.LoopBetween(2, 7), beep.LoopCrossfade(2))
	assert.NoError(t, err)
	assert.Equal(t, chunks, testtools.Collect(l))

	// Cross-check against the independent reference over parameter combinations,
	// including start/end sections and the tail samples after the section.
	for _, n := range []int{6, 10} {
		for _, start := range []int{0, 1} {
			for _, end := range []int{n - 1, n} {
				for _, times := range []int{0, 1, 3} {
					sectionLen := end - start
					for _, xfade := range []int{0, 1, 2, sectionLen / 2} {
						if xfade >= sectionLen {
							continue
						}
						ds, d := testtools.NewSequentialDataStreamer(n)
						l, err := beep.Loop2(ds, beep.LoopTimes(times), beep.LoopBetween(start, end), beep.LoopCrossfade(xfade))
						assert.NoError(t, err)
						assert.Equal(t,
							referenceLoop2Clean(d, start, end, times, xfade),
							testtools.Collect(l),
							"n=%d start=%d end=%d times=%d xfade=%d", n, start, end, times, xfade)
					}
				}
			}
		}
	}

	// Overlapping cross-fade zones (xfade > section/2) are still deterministic
	// and in-bounds. Section [0,4), xfade 3: every repetition after the first
	// contains only the blended tail.
	s, data = testtools.NewSequentialDataStreamer(4)
	l, err = beep.Loop2(s, beep.LoopTimes(1), beep.LoopCrossfade(3))
	assert.NoError(t, err)
	g1, g2 := 0.5, 1.0
	blend := func(tail, head [2]float64, g float64) [2]float64 {
		return [2]float64{tail[0]*(1-g) + head[0]*g, tail[1]*(1-g) + head[1]*g}
	}
	want = [][2]float64{
		data[0], blend(data[1], data[0], 0), blend(data[2], data[1], g1), blend(data[3], data[2], g2), // blended repetition
		data[3], // final, unblended repetition from start+3
	}
	assert.Equal(t, want, testtools.Collect(l))

	// Zero repetitions: the source is played once and no head data is mixed in.
	s, data = testtools.NewSequentialDataStreamer(4)
	l, err = beep.Loop2(s, beep.LoopTimes(0), beep.LoopCrossfade(2))
	assert.NoError(t, err)
	assert.Equal(t, data, testtools.Collect(l))

	// Empty source stream: construction still reports the same error as without
	// the cross-fade option.
	empty := testtools.NewDataStreamer(nil)
	_, err = beep.Loop2(empty, beep.LoopCrossfade(1))
	assert.EqualError(t, err, "invalid argument to Loop2; start position 0 must be smaller than the source streamer length 0")

	// Infinite loop with cross-fading never drains and keeps producing the
	// periodic blended seam. Its finite prefix matches the reference output.
	inf, infData := testtools.NewSequentialDataStreamer(6)
	l, err = beep.Loop2(inf, beep.LoopCrossfade(3))
	assert.NoError(t, err)
	assert.Equal(t,
		referenceLoop2Clean(infData, 0, 6, -1, 3),
		testtools.CollectNum(15, l))

	// Infinite loop with cross-fading: the underlying stream errors inside the
	// first cross-fade zone (it has only 6 samples and the zone is the last 2),
	// so only the samples before the zone can be emitted; the error is then
	// propagated and the streamer drained.
	expectedErr := errors.New("expected error")
	es, _ := testtools.NewSequentialDataStreamer(6)
	es = testtools.NewDelayedErrorStreamer(es, 6, expectedErr)
	l, err = beep.Loop2(es, beep.LoopCrossfade(2))
	assert.NoError(t, err)
	buf := make([][2]float64, 10)
	n, ok := l.Stream(buf)
	assert.Equal(t, 4, n)
	assert.True(t, ok)
	assert.Equal(t, expectedErr, l.Err())
	n, ok = l.Stream(buf)
	assert.Equal(t, 0, n)
	assert.False(t, ok)

	// Seek error while entering a cross-faded repetition is stored and drained.
	ss, _ := testtools.NewSequentialDataStreamer(6)
	ss = testtools.NewSeekErrorStreamer(ss, expectedErr)
	l, err = beep.Loop2(ss, beep.LoopTimes(2), beep.LoopCrossfade(2))
	assert.NoError(t, err)
	got := testtools.Collect(l)
	assert.Equal(t, 4, len(got))
	assert.Equal(t, expectedErr, l.Err())

	// Correct return behavior of a finite cross-faded streamer.
	rs, _ := testtools.NewSequentialDataStreamer(40)
	l, err = beep.Loop2(rs, beep.LoopTimes(2), beep.LoopCrossfade(3))
	assert.NoError(t, err)
	expected := testtools.Collect(mustLoop2(t,
		func() beep.StreamSeeker { s, _ := testtools.NewSequentialDataStreamer(40); return s }(),
		beep.LoopTimes(2), beep.LoopCrossfade(3)))
	testtools.AssertStreamerHasCorrectReturnBehaviour(t, l, len(expected))
}

func mustLoop2(t *testing.T, s beep.StreamSeeker, opts ...beep.LoopOption) beep.Streamer {
	t.Helper()
	l, err := beep.Loop2(s, opts...)
	assert.NoError(t, err)
	return l
}

func TestSeq(t *testing.T) {
	var (
		n    = 7
		s    = make([]beep.Streamer, n)
		data = make([][][2]float64, n)
	)
	for i := range s {
		s[i], data[i] = testtools.RandomDataStreamer(rand.Intn(1e5) + 1e4)
	}

	var want [][2]float64
	for _, d := range data {
		want = append(want, d...)
	}

	got := testtools.Collect(beep.Seq(s...))

	if !reflect.DeepEqual(want, got) {
		t.Errorf("Seq not working properly")
	}
}

func TestMix(t *testing.T) {
	var (
		n    = 7
		s    = make([]beep.Streamer, n)
		data = make([][][2]float64, n)
	)
	for i := range s {
		s[i], data[i] = testtools.RandomDataStreamer(rand.Intn(1e5) + 1e4)
	}

	maxLen := 0
	for _, d := range data {
		maxLen = max(maxLen, len(d))
	}

	want := make([][2]float64, maxLen)
	for _, d := range data {
		for i := range d {
			want[i][0] += d[i][0]
			want[i][1] += d[i][1]
		}
	}

	got := testtools.Collect(beep.Mix(s...))

	testtools.AssertSamplesEqual(t, want, got)
}

func TestDup(t *testing.T) {
	for i := 0; i < 7; i++ {
		s, data := testtools.RandomDataStreamer(rand.Intn(1e5) + 1e4)
		st, su := beep.Dup(s)

		var tData, uData [][2]float64
		for {
			buf := make([][2]float64, rand.Intn(1e4))
			tn, tok := st.Stream(buf)
			tData = append(tData, buf[:tn]...)

			buf = make([][2]float64, rand.Intn(1e4))
			un, uok := su.Stream(buf)
			uData = append(uData, buf[:un]...)

			if !tok && !uok {
				break
			}
		}

		if !reflect.DeepEqual(data, tData) || !reflect.DeepEqual(data, uData) {
			t.Error("Dup not working correctly")
		}
	}
}
