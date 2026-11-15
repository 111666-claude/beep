package beep

// Scheduled is a handle to a Streamer scheduled on a Mixer with Schedule. It allows cancelling
// the scheduled Streamer before or during playback.
type Scheduled interface {
	// Cancel removes the scheduled Streamer from the Mixer.
	//
	// If the Streamer has not started playing yet, it will never be sampled. If it has already
	// started, it stops contributing to the Mixer output at the next Stream call; samples already
	// mixed into previously returned buffers are unaffected.
	//
	// Cancelling more than once, or cancelling a Streamer which has already finished playing and
	// been removed from the Mixer, has no effect.
	Cancel()
}

// Schedule schedules s to start playing at the absolute sample position start on the Mixer's
// sample clock (see Mixer.Position). The first sample ever produced by the Mixer is at position 0.
//
// The Streamer is not sampled until the clock reaches start: requesting any number of samples per
// Stream call does not shift its position, and samples before start are filled with silence.
// Several Streamers may be scheduled at the same position; they are mixed in the order they were
// scheduled, and overlapping Streamers are added together like Streamers added with Add.
//
// If start is at or before the current clock position (including past positions), the Streamer
// starts on the next Stream call, beginning with its first sample.
//
// The returned Scheduled handle can be used to cancel the Streamer before or during playback.
// Finished and cancelled Streamers are removed from the Mixer automatically, so long-running
// scheduling does not retain completed Streamers.
//
// A Streamer must not be scheduled on more than one Mixer, or scheduled twice, at the same time.
func (m *Mixer) Schedule(start int64, s Streamer) Scheduled {
	if start < 0 {
		panic("beep: cannot schedule a Streamer at a negative sample position")
	}
	e := &scheduledStreamer{
		s:     s,
		start: start,
	}
	m.scheduled = append(m.scheduled, e)
	return e
}

// scheduledStreamer wraps a Streamer which starts at an absolute mixer position. While it waits
// for its start position it produces silence without touching the wrapped Streamer, so a
// Streamer cancelled before its start is never sampled.
type scheduledStreamer struct {
	s        Streamer
	start    int64 // absolute mixer position at which the first sample of s is played
	delay    int   // samples of silence left before the first sample of s is played
	started  bool  // the entry has been activated and moved into m.streamers
	canceled bool
}

// Stream emits the remaining leading silence, then forwards to the wrapped Streamer.
func (e *scheduledStreamer) Stream(samples [][2]float64) (n int, ok bool) {
	if e.canceled {
		return 0, false
	}
	if e.delay > 0 {
		silence := min(e.delay, len(samples))
		clear(samples[:silence])
		e.delay -= silence
		samples = samples[silence:]
		n += silence
		if len(samples) == 0 {
			return n, true
		}
	}

	sn, sok := e.s.Stream(samples)
	n += sn
	// Stick to the Streamer contract: a call that produced samples returns ok == true, even if the
	// wrapped Streamer drained within it; the following call then returns (0, false), which lets the
	// Mixer remove the entry — the same two-step drain as a Streamer added with Add.
	if n > 0 {
		return n, true
	}
	return 0, sok
}

// Err propagates the wrapped Streamer's error.
func (e *scheduledStreamer) Err() error {
	return e.s.Err()
}

// Cancel removes the scheduled Streamer. See the Scheduled interface for details.
func (e *scheduledStreamer) Cancel() {
	if e.canceled {
		return
	}
	e.canceled = true
}
