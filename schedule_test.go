package beep_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/internal/testtools"
)

// pulseStreamer streams length samples, each equal to [value, value], and records how many times
// it was sampled. It models a short one-shot sound effect.
type pulseStreamer struct {
	value  float64
	length int
	calls  int
}

func (p *pulseStreamer) Stream(samples [][2]float64) (n int, ok bool) {
	p.calls++
	if p.length == 0 {
		return 0, false
	}
	n = min(p.length, len(samples))
	for i := range samples[:n] {
		samples[i] = [2]float64{p.value, p.value}
	}
	p.length -= n
	if p.length == 0 {
		return n, true
	}
	return n, true
}

func (p *pulseStreamer) Err() error { return nil }

// collectAt reads total samples from s using the given block size and returns them.
func collectAt(t *testing.T, s beep.Streamer, blockSize, total int) [][2]float64 {
	t.Helper()
	buf := make([][2]float64, blockSize)
	out := make([][2]float64, 0, total)
	for len(out) < total {
		toRead := min(blockSize, total-len(out))
		n, ok := s.Stream(buf[:toRead])
		require.True(t, ok, "mixer must not drain while samples are requested (KeepAlive default)")
		require.Equal(t, toRead, n, "mixer must fill the full request")
		out = append(out, buf[:n]...)
	}
	return out
}

// blockSizes spans values both below and above the Mixer's internal 512-sample working buffer,
// including 1 (single-sample pulls), prime numbers and large buffers, so that every scheduling
// boundary is exercised against several chunkings.
var blockSizes = []int{1, 2, 3, 7, 31, 100, 257, 511, 512, 513, 1000, 2048}

// TestMixer_ScheduleStartsAtExactPosition checks that a scheduled pulse begins exactly at its
// absolute sample position regardless of the block size used to pull samples from the Mixer.
func TestMixer_ScheduleStartsAtExactPosition(t *testing.T) {
	const (
		start = 300

		length = 50
		value  = 0.5
		total  = 500
	)

	for _, block := range blockSizes {
		t.Run("", func(t *testing.T) {
			m := &beep.Mixer{}
			p := &pulseStreamer{value: value, length: length}
			m.Schedule(start, p)

			assert.Equal(t, int64(0), m.Position())
			out := collectAt(t, m, block, total)
			require.Len(t, out, total)
			assert.Equal(t, int64(total), m.Position())

			for i, s := range out {
				var want [2]float64
				if i >= start && i < start+length {
					want = [2]float64{value, value}
				}
				assert.InDelta(t, want[0], s[0], 1e-12, "block=%d sample=%d", block, i)
				assert.InDelta(t, want[1], s[1], 1e-12, "block=%d sample=%d", block, i)
			}
		})
	}
}

// TestMixer_ScheduleOverlapMixes checks that two scheduled pulses overlapping in time are added
// together, and that non-overlapping sections keep their own values.
func TestMixer_ScheduleOverlapMixes(t *testing.T) {
	const total = 400

	for _, block := range blockSizes {
		t.Run("", func(t *testing.T) {
			m := &beep.Mixer{}
			p1 := &pulseStreamer{value: 0.3, length: 100}
			p2 := &pulseStreamer{value: 0.2, length: 100}
			m.Schedule(50, p1)  // active on [50, 150)
			m.Schedule(120, p2) // active on [120, 220)

			out := collectAt(t, m, block, total)
			for i, s := range out {
				var want float64
				switch {
				case i >= 50 && i < 120:
					want = 0.3
				case i >= 120 && i < 150:
					want = 0.3 + 0.2 // overlap
				case i >= 150 && i < 220:
					want = 0.2
				}
				assert.InDelta(t, want, s[0], 1e-12, "block=%d sample=%d", block, i)
				assert.InDelta(t, want, s[1], 1e-12, "block=%d sample=%d", block, i)
			}
		})
	}
}

// TestMixer_ScheduleSamePositionOrder verifies that several Streamers scheduled at the same
// position are all started and mixed (the trigger order is stable, matching scheduling order).
func TestMixer_ScheduleSamePositionOrder(t *testing.T) {
	const (
		start  = 64
		length = 16
		total  = 128
	)

	order := make([]int, 0, 3)
	mk := func(id int, val float64) beep.Streamer {
		p := &pulseStreamer{value: val, length: length}
		return beep.StreamerFunc(func(samples [][2]float64) (int, bool) {
			if p.calls == 0 {
				order = append(order, id)
			}
			return p.Stream(samples)
		})
	}

	m := &beep.Mixer{}
	m.Schedule(start, mk(1, 0.1))
	m.Schedule(start, mk(2, 0.2))
	m.Schedule(start, mk(3, 0.3))

	out := collectAt(t, m, 37, total)
	for i := start; i < start+length; i++ {
		assert.InDelta(t, 0.6, out[i][0], 1e-12, "sample %d", i)
	}
	assert.Equal(t, []int{1, 2, 3}, order, "same-position streamers must trigger in scheduling order")
	assert.Equal(t, 0, m.Len(), "all pulses should have drained and been removed")
}

// TestMixer_ScheduleCancelBeforeStart verifies a cancelled, not-yet-started Streamer is never
// sampled and never contributes any samples.
func TestMixer_ScheduleCancelBeforeStart(t *testing.T) {
	m := &beep.Mixer{}
	p := &pulseStreamer{value: 1, length: 50}
	h := m.Schedule(200, p)

	out := testtools.CollectNum(100, m)
	assert.Len(t, out, 100)
	assert.Equal(t, 0, p.calls, "streamer must not be sampled before its start position")

	h.Cancel()
	out = testtools.CollectNum(200, m)
	assert.Len(t, out, 200)
	assert.Equal(t, 0, p.calls, "cancelled streamer must never be sampled")
	for _, s := range out {
		assert.Equal(t, [2]float64{}, s)
	}

	// Cancelling again is a no-op.
	assert.NotPanics(t, func() { h.Cancel() })
}

// TestMixer_ScheduleCancelDuringPlayback verifies a cancelled, already-started Streamer stops
// contributing on the next Stream call.
func TestMixer_ScheduleCancelDuringPlayback(t *testing.T) {
	const total = 200
	m := &beep.Mixer{}
	p := &pulseStreamer{value: 0.7, length: 10000} // long pulse, cancelled before it drains
	h := m.Schedule(10, p)

	out := testtools.CollectNum(50, m) // plays samples 0..49; pulse active on 10..49
	for i := 10; i < 50; i++ {
		assert.InDelta(t, 0.7, out[i][0], 1e-12)
	}
	assert.Greater(t, p.calls, 0)

	h.Cancel()
	out = testtools.CollectNum(150, m)
	for i, s := range out {
		assert.Equal(t, [2]float64{}, s, "sample %d must be silent after cancel", i)
	}
	assert.Equal(t, int64(total), m.Position())
}

// TestMixer_SchedulePastPosition verifies a Streamer scheduled at or before the current clock
// position starts immediately, beginning with its first sample.
func TestMixer_SchedulePastPosition(t *testing.T) {
	m := &beep.Mixer{}
	testtools.CollectNum(100, m) // advance clock to 100
	assert.Equal(t, int64(100), m.Position())

	p := &pulseStreamer{value: 0.9, length: 20}
	m.Schedule(100, p) // exactly the current position
	out := testtools.CollectNum(30, m)
	for i := 0; i < 20; i++ {
		assert.InDelta(t, 0.9, out[i][0], 1e-12)
	}
	for i := 20; i < 30; i++ {
		assert.Equal(t, [2]float64{}, out[i])
	}

	p2 := &pulseStreamer{value: 0.4, length: 10}
	m.Schedule(50, p2) // already in the past
	out = testtools.CollectNum(15, m)
	for i := 0; i < 10; i++ {
		assert.InDelta(t, 0.4, out[i][0], 1e-12)
	}
}

// TestMixer_ScheduleNegativePositionPanics verifies the clock origin is documented by rejecting
// negative positions.
func TestMixer_ScheduleNegativePositionPanics(t *testing.T) {
	m := &beep.Mixer{}
	assert.Panics(t, func() {
		m.Schedule(-1, &pulseStreamer{value: 1, length: 1})
	})
}

// TestMixer_ScheduleKeepsAlive verifies that a pending scheduled Streamer prevents a
// KeepAlive(false) Mixer from draining, and that it drains once everything is done.
func TestMixer_ScheduleKeepsAlive(t *testing.T) {
	m := &beep.Mixer{}
	m.KeepAlive(false)
	p := &pulseStreamer{value: 0.5, length: 10}
	m.Schedule(100, p)

	// Before the start position the Mixer must still produce silence instead of draining.
	out := testtools.CollectNum(100, m)
	assert.Len(t, out, 100)
	assert.Equal(t, 0, p.calls)

	// The pulse plays at its scheduled position.
	out = testtools.CollectNum(10, m)
	assert.Len(t, out, 10)
	for _, s := range out {
		assert.InDelta(t, 0.5, s[0], 1e-12)
	}

	// One more Stream call lets the drained pulse report !ok and be removed, after which the Mixer
	// itself reports drained — the same two-step drain semantics as a Streamer added with Add.
	_, _ = m.Stream(make([][2]float64, 1))
	n, ok := m.Stream(make([][2]float64, 16))
	assert.False(t, ok)
	assert.Equal(t, 0, n)
}

// TestMixer_ScheduleCancelledPendingDoesNotKeepAlive verifies that cancelling the only pending
// Streamer lets a KeepAlive(false) Mixer drain.
func TestMixer_ScheduleCancelledPendingDoesNotKeepAlive(t *testing.T) {
	m := &beep.Mixer{}
	m.KeepAlive(false)
	h := m.Schedule(1000, &pulseStreamer{value: 1, length: 10})

	testtools.CollectNum(10, m)
	h.Cancel()

	n, ok := m.Stream(make([][2]float64, 16))
	assert.False(t, ok)
	assert.Equal(t, 0, n)
}

// TestMixer_ScheduleEmptyStreamer verifies a scheduled Streamer which drains immediately does not
// linger and does not disturb surrounding samples.
func TestMixer_ScheduleEmptyStreamer(t *testing.T) {
	m := &beep.Mixer{}
	m.Schedule(20, beep.StreamerFunc(func([][2]float64) (int, bool) {
		return 0, false
	}))
	out := testtools.CollectNum(50, m)
	assert.Len(t, out, 50)
	for _, s := range out {
		assert.Equal(t, [2]float64{}, s)
	}
	assert.Equal(t, 0, m.Len())
}

// TestMixer_ScheduleWithAdd verifies scheduled Streamers mix with Streamers added via Add and that
// the original Add behaviour is unaffected.
func TestMixer_ScheduleWithAdd(t *testing.T) {
	m := &beep.Mixer{}
	immediate := &pulseStreamer{value: 0.1, length: 300}
	scheduled := &pulseStreamer{value: 0.2, length: 100}
	m.Add(immediate)
	m.Schedule(100, scheduled)

	// Read one sample past the longest pulse so both have drained and been removed.
	out := testtools.CollectNum(301, m)
	for i, s := range out[:250] {
		var want float64
		if i < 300 {
			want += 0.1
		}
		if i >= 100 && i < 200 {
			want += 0.2
		}
		assert.InDelta(t, want, s[0], 1e-12, "sample %d", i)
	}
	assert.Equal(t, 0, m.Len(), "both streamers should have drained and been removed")
}

// TestMixer_ScheduleClear verifies Clear removes pending and playing scheduled Streamers but keeps
// the sample clock running.
func TestMixer_ScheduleClear(t *testing.T) {
	m := &beep.Mixer{}
	pending := &pulseStreamer{value: 1, length: 10}
	playing := &pulseStreamer{value: 1, length: 1000}
	m.Schedule(500, pending)
	m.Schedule(0, playing)

	testtools.CollectNum(50, m)
	assert.Greater(t, playing.calls, 0)
	assert.Equal(t, 0, pending.calls)

	m.Clear()
	assert.Equal(t, int64(50), m.Position(), "Clear must not reset the sample clock")

	out := testtools.CollectNum(600, m)
	assert.Len(t, out, 600)
	for _, s := range out {
		assert.Equal(t, [2]float64{}, s)
	}
	assert.Equal(t, 0, pending.calls)
}

// TestMixer_ScheduleExactBlockSizes drives one pulse through every block size and checks the first
// and last active sample index explicitly, covering the 512-sample internal buffer boundary.
func TestMixer_ScheduleExactBlockSizes(t *testing.T) {
	const (
		start  = 700
		length = 30
		total  = 800
	)
	for _, block := range blockSizes {
		t.Run("", func(t *testing.T) {
			m := &beep.Mixer{}
			m.Schedule(start, &pulseStreamer{value: 1, length: length})
			out := collectAt(t, m, block, total)

			first, last := -1, -1
			for i, s := range out {
				if s[0] != 0 {
					if first == -1 {
						first = i
					}
					last = i
				}
			}
			assert.Equal(t, start, first, "block=%d wrong first sample", block)
			assert.Equal(t, start+length-1, last, "block=%d wrong last sample", block)
		})
	}
}
