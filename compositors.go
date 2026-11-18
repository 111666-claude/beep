package beep

import (
	"fmt"
	"math"

	"github.com/pkg/errors"
)

// Take returns a Streamer which streams at most num samples from s.
//
// The returned Streamer propagates s's errors through Err.
func Take(num int, s Streamer) Streamer {
	return &take{
		s:       s,
		remains: num,
	}
}

type take struct {
	s       Streamer
	remains int
}

func (t *take) Stream(samples [][2]float64) (n int, ok bool) {
	if t.remains <= 0 {
		return 0, false
	}
	toStream := min(t.remains, len(samples))
	n, ok = t.s.Stream(samples[:toStream])
	t.remains -= n
	return n, ok
}

func (t *take) Err() error {
	return t.s.Err()
}

// Loop takes a StreamSeeker and plays it count times. If count is negative, s is looped infinitely.
//
// The returned Streamer propagates s's errors.
//
// Deprecated: use Loop2 instead. A call to Loop can be rewritten as follows:
// - beep.Loop(-1, s) -> beep.Loop2(s)
// - beep.Loop(0, s) -> no longer supported, use beep.Ctrl instead.
// - beep.Loop(3, s) -> beep.Loop2(s, beep.LoopTimes(2))
// Note that beep.LoopTimes takes the number of repeats instead of the number of total plays.
func Loop(count int, s StreamSeeker) Streamer {
	return &loop{
		s:       s,
		remains: count,
	}
}

type loop struct {
	s       StreamSeeker
	remains int
}

func (l *loop) Stream(samples [][2]float64) (n int, ok bool) {
	if l.remains == 0 || l.s.Err() != nil {
		return 0, false
	}
	for len(samples) > 0 {
		sn, sok := l.s.Stream(samples)
		if !sok {
			if l.remains > 0 {
				l.remains--
			}
			if l.remains == 0 {
				break
			}
			err := l.s.Seek(0)
			if err != nil {
				return n, true
			}
			continue
		}
		samples = samples[sn:]
		n += sn
	}
	return n, true
}

func (l *loop) Err() error {
	return l.s.Err()
}

type LoopOption func(opts *loop2)

// LoopTimes sets how many times the source stream will repeat. If a section is defined
// by LoopStart, LoopEnd, or LoopBetween, only that section will repeat.
// A value of 0 plays the stream or section once (no repetition); 1 plays it twice, and so on.
func LoopTimes(times int) LoopOption {
	if times < 0 {
		panic("invalid argument to LoopTimes; times cannot be negative")
	}
	return func(loop *loop2) {
		loop.remains = times
	}
}

// LoopStart sets the position in the source stream to which it returns (using Seek())
// after reaching the end of the stream or the position set using LoopEnd. The samples
// before this position are played once before the loop begins.
func LoopStart(pos int) LoopOption {
	if pos < 0 {
		panic("invalid argument to LoopStart; pos cannot be negative")
	}
	return func(loop *loop2) {
		loop.start = pos
	}
}

// LoopEnd sets the position (exclusive) in the source stream up to which the stream plays
// before returning (seeking) back to the start of the stream or the position set by LoopStart.
// The samples after this position are played once after looping completes.
func LoopEnd(pos int) LoopOption {
	if pos < 0 {
		panic("invalid argument to LoopEnd; pos cannot be negative")
	}
	return func(loop *loop2) {
		loop.end = pos
	}
}

// LoopBetween sets both the LoopStart and LoopEnd positions simultaneously, specifying
// the section of the stream that will be looped.
func LoopBetween(start, end int) LoopOption {
	return func(opts *loop2) {
		LoopStart(start)(opts)
		LoopEnd(end)(opts)
	}
}

// LoopCrossfade enables cross-fading of length samples at every seam where the
// looping section jumps back to its start. At each seam, the last length samples
// of the section are blended frame-by-frame (the two channels are mixed
// independently) with the first length samples of the following repetition,
// using linear gains that sum to 1 at every frame. This hides the click caused
// by differing sample levels at the beginning and the end of the section.
//
// Cross-fading is length-neutral: the blended-in head of the next repetition
// replaces the samples that would otherwise be replayed after the seek, so the
// total number of output samples is the same as without cross-fading and does
// not depend on the Stream buffer size. When LoopTimes limits the number of
// repetitions, no cross-fade is applied at the end of the last repetition; the
// tail of the source is played unchanged and no head samples are mixed into it.
//
// A length of 0 (the default) disables cross-fading and leaves the looping
// behavior unchanged. The length must be strictly smaller than the loop section
// defined by LoopStart, LoopEnd, or LoopBetween; otherwise Loop2 returns an
// error.
func LoopCrossfade(length int) LoopOption {
	if length < 0 {
		panic("invalid argument to LoopCrossfade; length cannot be negative")
	}
	return func(loop *loop2) {
		loop.xfade = length
	}
}

// Loop2 takes a StreamSeeker and repeats it according to the specified options. If no LoopTimes
// option is provided, the stream loops indefinitely. LoopStart, LoopEnd, or LoopBetween can define
// a specific section of the stream to loop. Samples before the start and after the end positions
// are played once before and after the looping section, respectively.
//
// The returned Streamer propagates any errors from s.
func Loop2(s StreamSeeker, opts ...LoopOption) (Streamer, error) {
	l := &loop2{
		s:       s,
		remains: -1, // indefinitely
		start:   0,
		end:     math.MaxInt,
	}
	for _, opt := range opts {
		opt(l)
	}

	n := s.Len()
	if l.start >= n {
		return nil, errors.New(fmt.Sprintf("invalid argument to Loop2; start position %d must be smaller than the source streamer length %d", l.start, n))
	}
	if l.start >= l.end {
		return nil, errors.New(fmt.Sprintf("invalid argument to Loop2; start position %d must be smaller than the end position %d", l.start, l.end))
	}
	l.end = min(l.end, n)
	if l.xfade >= l.end-l.start {
		return nil, errors.New(fmt.Sprintf("invalid argument to Loop2; crossfade length %d must be smaller than the loop section length %d", l.xfade, l.end-l.start))
	}

	return l, nil
}

type loop2 struct {
	s       StreamSeeker
	remains int // number of seeks remaining.
	start   int // start position in the stream where looping begins. Samples before this position are played once before the first loop.
	end     int // end position in the stream where looping ends and restarts from `start`.
	xfade   int // number of samples cross-faded at every loop seam; 0 disables cross-fading.
	prefix  [][2]float64
	err     error
}

func (l *loop2) Stream(samples [][2]float64) (n int, ok bool) {
	if l.err != nil {
		return 0, false
	}
	for len(samples) > 0 {
		pos := l.s.Position()

		if pos >= l.end {
			// End of the looping section reached.
			if l.remains == 0 {
				// No more repetitions: continue past the section without cross-fading.
				sn, sok := l.s.Stream(samples)
				n += sn
				if sn < len(samples) || !sok {
					l.err = l.s.Err()
					return n, n > 0
				}
				samples = samples[sn:]
				continue
			}
			if l.remains > 0 {
				l.remains--
			}
			// Another repetition follows; jump past the cross-faded head.
			if err := l.s.Seek(l.start + l.xfade); err != nil {
				l.err = err
				return n, true
			}
			continue
		}

		// Cross-fading only happens while another repetition follows: on the
		// last repetition (l.remains == 0) the zone is passed through unchanged
		// so no head samples are mixed into the tail of a finite loop.
		if l.xfade == 0 || l.remains == 0 || pos < l.end-l.xfade {
			// Outside the cross-fade zone: pass the source samples through
			// unchanged. Stop at the zone boundary (when there is one) so that
			// the cross-fade branch handles every zone frame, regardless of the
			// size of the caller's buffer.
			toStream := min(l.end-pos, len(samples))
			if l.xfade > 0 && l.remains != 0 {
				toStream = min(l.end-l.xfade-pos, toStream)
			}
			sn, sok := l.s.Stream(samples[:toStream])
			n += sn
			if sn < toStream || !sok {
				l.err = l.s.Err()
				return n, n > 0
			}
			samples = samples[sn:]
			continue
		}

		// Inside the cross-fade zone. The zone is entered exactly once per
		// repetition; load and cache the head samples of the section on first entry.
		if l.prefix == nil {
			if err := l.loadPrefix(); err != nil {
				l.err = err
				return n, n > 0
			}
		}

		zoneOffset := pos - (l.end - l.xfade)
		toStream := min(l.end-pos, len(samples))
		sn, sok := l.s.Stream(samples[:toStream])
		n += sn
		if sn < toStream || !sok {
			l.err = l.s.Err()
			return n, n > 0
		}
		for i := 0; i < sn; i++ {
			k := zoneOffset + i
			g := l.fadeGain(k)
			head := l.prefix[k]
			samples[i][0] = samples[i][0]*(1-g) + head[0]*g
			samples[i][1] = samples[i][1]*(1-g) + head[1]*g
		}
		samples = samples[sn:]
	}
	return n, true
}

// loadPrefix reads the first xfade samples of the looping section into l.prefix
// and restores the source position afterwards.
func (l *loop2) loadPrefix() error {
	pos := l.s.Position()
	if err := l.s.Seek(l.start); err != nil {
		return err
	}
	l.prefix = make([][2]float64, l.xfade)
	read, ok := l.s.Stream(l.prefix)
	restoreErr := l.s.Seek(pos)
	if read < l.xfade || !ok {
		if err := l.s.Err(); err != nil {
			return err
		}
		return errors.New(fmt.Sprintf("invalid argument to Loop2; source stream ended before the %d crossfade samples could be read", l.xfade))
	}
	return restoreErr
}

// fadeGain returns the gain of the head sample at cross-fade frame k of a
// cross-fade with l.xfade frames. The tail sample uses the complementary gain
// 1-fadeGain(k), so the gains sum to 1 at every frame.
func (l *loop2) fadeGain(k int) float64 {
	if l.xfade == 1 {
		return 1
	}
	return float64(k) / float64(l.xfade-1)
}

func (l *loop2) Err() error {
	return l.err
}

// Seq takes zero or more Streamers and returns a Streamer which streams them one by one without pauses.
//
// Seq does not propagate errors from the Streamers.
func Seq(s ...Streamer) Streamer {
	i := 0
	return StreamerFunc(func(samples [][2]float64) (n int, ok bool) {
		for i < len(s) && len(samples) > 0 {
			sn, sok := s[i].Stream(samples)
			samples = samples[sn:]
			n, ok = n+sn, ok || sok
			if !sok {
				i++
			}
		}
		return n, ok
	})
}

// Mix takes zero or more Streamers and returns a Streamer which streams them mixed together.
//
// Mix does not propagate errors from the Streamers.
func Mix(s ...Streamer) Streamer {
	return &Mixer{
		streamers:     s,
		stopWhenEmpty: true,
	}
}

// Dup returns two Streamers which both stream the same data as the original s. The two Streamers
// can't be used concurrently without synchronization.
func Dup(s Streamer) (t, u Streamer) {
	var tBuf, uBuf [][2]float64
	return &dup{&tBuf, &uBuf, s}, &dup{&uBuf, &tBuf, s}
}

type dup struct {
	myBuf, itsBuf *[][2]float64
	s             Streamer
}

func (d *dup) Stream(samples [][2]float64) (n int, ok bool) {
	buf := *d.myBuf
	n = copy(samples, buf)
	ok = len(buf) > 0
	buf = buf[n:]
	samples = samples[n:]
	*d.myBuf = buf

	if len(samples) > 0 {
		sn, sok := d.s.Stream(samples)
		n += sn
		ok = ok || sok
		*d.itsBuf = append(*d.itsBuf, samples[:sn]...)
	}

	return n, ok
}

func (d *dup) Err() error {
	return d.s.Err()
}
