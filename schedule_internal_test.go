package beep

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMixer_ScheduleInternalCleanup(t *testing.T) {
	m := &Mixer{}

	// Schedule a burst of short pulses spread across a long timeline and pull all the samples.
	const count = 200
	const span = 5000
	handles := make([]Scheduled, count)
	for i := range handles {
		p := &pulseInternal{length: 5}
		handles[i] = m.Schedule(int64(i*span/count), p)
	}
	assert.Len(t, m.scheduled, count)

	buf := make([][2]float64, 123)
	for streamed := 0; streamed < span; {
		toStream := min(len(buf), span-streamed)
		n, ok := m.Stream(buf[:toStream])
		assert.True(t, ok)
		assert.Equal(t, toStream, n)
		streamed += n
	}

	// Everything has started and drained: no pending entries, no active streamers.
	assert.Empty(t, m.scheduled, "finished scheduled streamers must not be retained")
	assert.Empty(t, m.streamers, "drained scheduled streamers must be removed like Add'ed ones")
	assert.Equal(t, int64(span), m.position)

	// Handles remain callable after natural completion.
	for _, h := range handles {
		assert.NotPanics(t, func() { h.Cancel() })
	}
}

func TestMixer_ScheduleCancelCleansPendingList(t *testing.T) {
	m := &Mixer{}
	m.Schedule(1000, &pulseInternal{length: 1})
	h2 := m.Schedule(2000, &pulseInternal{length: 1})
	h3 := m.Schedule(3000, &pulseInternal{length: 1})

	h2.Cancel()
	// Advance past h1's start and let it drain.
	buf := make([][2]float64, 1100)
	n, ok := m.Stream(buf)
	assert.True(t, ok)
	assert.Equal(t, 1100, n)

	// Only h3 is still pending; the cancelled h2 was dropped and h1 was activated and drained.
	assert.Len(t, m.scheduled, 1)
	assert.Equal(t, int64(3000), m.scheduled[0].start)
	assert.Empty(t, m.streamers)

	h3.Cancel()
	m.activateScheduled(m.position + 1)
	assert.Empty(t, m.scheduled)
}

func TestMixer_ScheduleClearResetsScheduled(t *testing.T) {
	m := &Mixer{}
	m.Schedule(100, &pulseInternal{length: 1})
	m.Schedule(200, &pulseInternal{length: 1})
	m.Add(&pulseInternal{length: 1})

	m.Clear()
	assert.Empty(t, m.scheduled)
	assert.Empty(t, m.streamers)
	assert.Equal(t, int64(0), m.position, "Clear must not move the clock")
}

// pulseInternal is a tiny finite streamer for whitebox tests.
type pulseInternal struct {
	length int
}

func (p *pulseInternal) Stream(samples [][2]float64) (n int, ok bool) {
	if p.length == 0 {
		return 0, false
	}
	n = min(p.length, len(samples))
	for i := range samples[:n] {
		samples[i] = [2]float64{1, 1}
	}
	p.length -= n
	return n, true
}

func (p *pulseInternal) Err() error { return nil }
