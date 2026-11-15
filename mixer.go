package beep

// Mixer allows for dynamic mixing of arbitrary number of Streamers. Mixer automatically removes
// drained Streamers. Depending on the KeepAlive() setting, Stream will either play silence or
// drain when all Streamers have been drained. By default, Mixer keeps playing silence.
//
// Besides Streamers added with Add, which start playing on the next Stream call, a Mixer keeps an
// absolute sample clock (see Position) and can schedule Streamers to start at an arbitrary sample
// position via Schedule.
type Mixer struct {
	streamers     []Streamer
	scheduled     []*scheduledStreamer
	position      int64
	stopWhenEmpty bool
}

// KeepAlive configures the Mixer to either keep playing silence when all its Streamers have
// drained (keepAlive == true) or stop playing (keepAlive == false).
func (m *Mixer) KeepAlive(keepAlive bool) {
	m.stopWhenEmpty = !keepAlive
}

// Len returns the number of Streamers currently playing in the Mixer.
//
// Streamers scheduled with Schedule that have not started yet (or have been removed before
// starting) are not counted.
func (m *Mixer) Len() int {
	return len(m.streamers)
}

// Position returns the current value of the Mixer's sample clock: the total number of samples
// produced by Stream so far.
//
// The clock starts at zero when the Mixer is created and is only advanced by Stream; Add,
// Schedule, Clear and KeepAlive do not reset it. The position passed to Schedule is interpreted
// against this clock: the first sample produced by the Mixer has position 0.
func (m *Mixer) Position() int64 {
	return m.position
}

// Add adds Streamers to the Mixer. The Streamers start playing on the next call to Stream,
// regardless of the current sample clock position.
func (m *Mixer) Add(s ...Streamer) {
	m.streamers = append(m.streamers, s...)
}

// Clear removes all Streamers from the mixer, including both Streamers added with Add and
// Streamers scheduled with Schedule (whether or not they have started playing). The sample clock
// (see Position) is not reset.
func (m *Mixer) Clear() {
	for i := range m.streamers {
		m.streamers[i] = nil
	}
	m.streamers = m.streamers[:0]
	for i := range m.scheduled {
		m.scheduled[i] = nil
	}
	m.scheduled = m.scheduled[:0]
}

// hasWaiting reports whether any scheduled Streamer is still waiting for its start position.
// Cancelled entries do not keep the Mixer alive.
func (m *Mixer) hasWaiting() bool {
	for _, e := range m.scheduled {
		if e != nil && !e.started && !e.canceled {
			return true
		}
	}
	return false
}

// activateScheduled starts every scheduled Streamer whose start position falls within the chunk
// about to be streamed, which covers clock positions [m.position, chunkEnd). Entries are activated
// in scheduling order, so Streamers scheduled at the same position are mixed in the order they were
// scheduled. Entries removed before starting (by Cancel or Clear) are dropped here without ever
// being sampled.
func (m *Mixer) activateScheduled(chunkEnd int64) {
	kept := m.scheduled[:0]
	for _, e := range m.scheduled {
		if e == nil || e.canceled {
			continue
		}
		if !e.started && e.start < chunkEnd {
			e.started = true
			// The delay is always non-negative: entries with start < m.position are already due
			// and start right away (delay 0).
			e.delay = int(e.start - m.position)
			if e.delay < 0 {
				e.delay = 0
			}
			m.streamers = append(m.streamers, e)
		}
		if e.started {
			// Now owned by m.streamers; drop from the pending list.
			continue
		}
		kept = append(kept, e)
	}
	m.scheduled = kept
}

// Stream the samples of all Streamers currently in the Mixer mixed together. Depending on the
// KeepAlive() setting, Stream will either play silence or drain when all Streamers have been
// drained.
//
// Streamers scheduled with Schedule are started at their absolute sample position and mixed in
// together with the Streamers added with Add. While a scheduled Streamer is waiting for its
// position, it keeps the Mixer alive even when KeepAlive(false) is set.
func (m *Mixer) Stream(samples [][2]float64) (n int, ok bool) {
	if m.stopWhenEmpty && len(m.streamers) == 0 && !m.hasWaiting() {
		return 0, false
	}

	var tmp [512][2]float64

	for len(samples) > 0 {
		toStream := min(len(tmp), len(samples))

		// Start any Streamer whose scheduled position falls within this chunk.
		m.activateScheduled(m.position + int64(toStream))

		// Clear the samples
		clear(samples[:toStream])

		snMax := 0
		for si := 0; si < len(m.streamers); si++ {
			// Mix the stream
			sn, sok := m.streamers[si].Stream(tmp[:toStream])
			for i := range tmp[:sn] {
				samples[i][0] += tmp[i][0]
				samples[i][1] += tmp[i][1]
			}
			if sn > snMax {
				snMax = sn
			}

			if sn < toStream || !sok {
				// Remove drained streamer.
				// Check the length of m.streamers again in case the call to Stream()
				// had a callback which clears the Mixer.
				if len(m.streamers) > 0 {
					last := len(m.streamers) - 1
					m.streamers[si] = m.streamers[last]
					m.streamers[last] = nil
					m.streamers = m.streamers[:last]
					si--
				}

				if m.stopWhenEmpty && len(m.streamers) == 0 && !m.hasWaiting() {
					m.position += int64(snMax)
					return n + snMax, true
				}
			}
		}

		samples = samples[toStream:]
		n += toStream
		m.position += int64(toStream)
	}

	return n, true
}

// Err always returns nil for Mixer.
//
// There are two reasons. The first one is that erroring Streamers are immediately drained and
// removed from the Mixer. The second one is that one Streamer shouldn't break the whole Mixer and
// you should handle the errors right where they can happen.
func (m *Mixer) Err() error {
	return nil
}
