package effects

import (
	"errors"
	"fmt"
	"math"
	"math/bits"

	"github.com/gopxl/beep/v2"
)

// reverbPartitionSize is the partition size of the uniformly partitioned
// convolution used by Reverb. All latency and buffering is a multiple of it,
// but the samples Reverb streams are independent of the requested buffer size.
const reverbPartitionSize = 1024

// DefaultMaxIRLength is the default maximum impulse response length, in samples
// per channel, accepted by NewReverb and NewMonoReverb. It can be changed with
// the WithReverbMaxIRLength option.
//
// The limit exists because a partitioned convolver precomputes and keeps
// frequency-domain data proportional to the impulse response length: roughly
// 64 bytes per impulse response sample per channel (about 128 bytes per sample
// for a stereo impulse response).
const DefaultMaxIRLength = 1 << 20

var (
	// ErrEmptyImpulseResponse is returned when the impulse response contains no
	// samples.
	ErrEmptyImpulseResponse = errors.New("effects: reverb impulse response must contain at least one sample")
	// ErrImpulseResponseTooLong is returned when the impulse response is longer
	// than the configured maximum (DefaultMaxIRLength or WithReverbMaxIRLength).
	ErrImpulseResponseTooLong = errors.New("effects: reverb impulse response exceeds the maximum allowed length")
	// ErrNonFiniteImpulseResponse is returned when the impulse response contains
	// a NaN or infinite sample.
	ErrNonFiniteImpulseResponse = errors.New("effects: reverb impulse response contains a non-finite sample")
	// ErrInvalidWetDryRatio is returned when the wet/dry ratio is NaN or outside
	// the [0, 1] range.
	ErrInvalidWetDryRatio = errors.New("effects: reverb wet/dry ratio must be a finite value between 0 and 1")
	// ErrInvalidMaxIRLength is returned when a non-positive maximum impulse
	// response length is configured.
	ErrInvalidMaxIRLength = errors.New("effects: reverb maximum impulse response length must be positive")
)

// ReverbOption configures a Reverb created by NewReverb or NewMonoReverb.
type ReverbOption func(*reverbConfig)

type reverbConfig struct {
	keepTail    bool
	maxIRLength int
}

// WithReverbTail controls whether the reverb keeps streaming the decaying tail
// of the convolution after the wrapped Streamer is drained.
//
// When enabled, the reverb streams exactly len(impulseResponse)-1 additional
// samples after the input ends, so the total number of streamed samples is
// len(input)+len(impulseResponse)-1. When disabled (the default), the reverb
// drains together with the wrapped Streamer and the tail is cut off.
func WithReverbTail(enabled bool) ReverbOption {
	return func(c *reverbConfig) {
		c.keepTail = enabled
	}
}

// WithReverbMaxIRLength sets the maximum accepted impulse response length, in
// samples per channel. Longer impulse responses make NewReverb and
// NewMonoReverb fail with ErrImpulseResponseTooLong. The value must be
// positive. See DefaultMaxIRLength for the motivation behind the limit.
func WithReverbMaxIRLength(n int) ReverbOption {
	return func(c *reverbConfig) {
		c.maxIRLength = n
	}
}

// NewReverb creates a Reverb which convolves the samples streamed by s with
// the given finite stereo impulse response, e.g. a recorded room or hall
// impulse response, and mixes the result with the original signal.
//
// Each channel is convolved independently: the left output channel is the
// convolution of the left input channel with the left impulse response
// channel, and likewise for the right channel. For a mono impulse response,
// use NewMonoReverb.
//
// The wetDry ratio controls the mix: 0 streams only the original (dry) signal,
// 1 streams only the convolved (wet) signal, and values in between mix both
// linearly. It must be a finite value within [0, 1].
//
// The impulse response must be non-empty, finite and no longer than the
// configured maximum (DefaultMaxIRLength unless changed with
// WithReverbMaxIRLength). Invalid input makes NewReverb return one of
// ErrEmptyImpulseResponse, ErrImpulseResponseTooLong,
// ErrNonFiniteImpulseResponse or ErrInvalidWetDryRatio.
//
// The returned Reverb propagates s's errors through Err. If s is a
// beep.StreamSeeker, Reverb can be seeked as well; seeking resets the
// convolution state, so the signal restarts without any reverb tail left over
// from before the seek.
func NewReverb(s beep.Streamer, impulseResponse [][2]float64, wetDry float64, opts ...ReverbOption) (*Reverb, error) {
	cfg, err := newReverbConfig(opts)
	if err != nil {
		return nil, err
	}
	if err := validateWetDry(wetDry); err != nil {
		return nil, err
	}
	if len(impulseResponse) == 0 {
		return nil, ErrEmptyImpulseResponse
	}
	if len(impulseResponse) > cfg.maxIRLength {
		return nil, fmt.Errorf("%w: %d samples (limit %d)", ErrImpulseResponseTooLong, len(impulseResponse), cfg.maxIRLength)
	}
	irLeft := make([]float64, len(impulseResponse))
	irRight := make([]float64, len(impulseResponse))
	for i, sample := range impulseResponse {
		if !isFinite(sample[0]) || !isFinite(sample[1]) {
			return nil, fmt.Errorf("%w at index %d", ErrNonFiniteImpulseResponse, i)
		}
		irLeft[i], irRight[i] = sample[0], sample[1]
	}
	return newReverb(s, irLeft, irRight, wetDry, cfg), nil
}

// NewMonoReverb creates a Reverb which convolves both channels of the samples
// streamed by s with the given finite mono impulse response. It is a
// convenience wrapper around NewReverb; see its documentation for details.
func NewMonoReverb(s beep.Streamer, impulseResponse []float64, wetDry float64, opts ...ReverbOption) (*Reverb, error) {
	cfg, err := newReverbConfig(opts)
	if err != nil {
		return nil, err
	}
	if err := validateWetDry(wetDry); err != nil {
		return nil, err
	}
	if len(impulseResponse) == 0 {
		return nil, ErrEmptyImpulseResponse
	}
	if len(impulseResponse) > cfg.maxIRLength {
		return nil, fmt.Errorf("%w: %d samples (limit %d)", ErrImpulseResponseTooLong, len(impulseResponse), cfg.maxIRLength)
	}
	for i, sample := range impulseResponse {
		if !isFinite(sample) {
			return nil, fmt.Errorf("%w at index %d", ErrNonFiniteImpulseResponse, i)
		}
	}
	return newReverb(s, impulseResponse, impulseResponse, wetDry, cfg), nil
}

func newReverbConfig(opts []ReverbOption) (reverbConfig, error) {
	cfg := reverbConfig{maxIRLength: DefaultMaxIRLength}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.maxIRLength < 1 {
		return reverbConfig{}, fmt.Errorf("%w, got %d", ErrInvalidMaxIRLength, cfg.maxIRLength)
	}
	return cfg, nil
}

func validateWetDry(wetDry float64) error {
	if math.IsNaN(wetDry) || wetDry < 0 || wetDry > 1 {
		return fmt.Errorf("%w, got %v", ErrInvalidWetDryRatio, wetDry)
	}
	return nil
}

func isFinite(x float64) bool {
	return !math.IsNaN(x) && !math.IsInf(x, 0)
}

// Reverb is a convolution reverb effect created by NewReverb or NewMonoReverb.
// See their documentation for details.
type Reverb struct {
	s beep.Streamer

	dry float64
	wet float64

	keepTail bool
	irLen    int

	conv [2]convolver

	in    [][2]float64 // input samples accumulated for the next partition
	inLen int
	out   [][2]float64 // processed samples ready to be streamed

	blockL []float64 // scratch, one partition of deinterleaved input
	blockR []float64

	drained  bool // the wrapped Streamer is drained
	tailLeft int  // tail samples still to produce

	err error
}

func newReverb(s beep.Streamer, irLeft, irRight []float64, wetDry float64, cfg reverbConfig) *Reverb {
	return &Reverb{
		s:        s,
		dry:      1 - wetDry,
		wet:      wetDry,
		keepTail: cfg.keepTail,
		irLen:    len(irLeft),
		conv: [2]convolver{
			newConvolver(irLeft),
			newConvolver(irRight),
		},
		in:     make([][2]float64, reverbPartitionSize),
		blockL: make([]float64, reverbPartitionSize),
		blockR: make([]float64, reverbPartitionSize),
	}
}

// Stream streams the convolved and mixed samples. The number of streamed
// samples only depends on the input and the impulse response, never on the
// size of the requested buffer.
func (r *Reverb) Stream(samples [][2]float64) (n int, ok bool) {
	if r.err != nil {
		return 0, false
	}
	for n < len(samples) {
		if len(r.out) == 0 {
			if !r.produce() {
				break
			}
			if r.err != nil {
				// The wrapped Streamer failed. Become drained immediately, as
				// required by the Streamer interface.
				return 0, false
			}
		}
		copied := copy(samples[n:], r.out)
		r.out = r.out[copied:]
		n += copied
	}
	if n == 0 {
		return 0, false
	}
	return n, true
}

// Err propagates the wrapped Streamer's errors.
func (r *Reverb) Err() error {
	if r.err != nil {
		return r.err
	}
	return r.s.Err()
}

// Seek seeks the wrapped Streamer and resets the convolution state, so no
// reverb tail from before the seek bleeds into the signal after it. It returns
// an error if the wrapped Streamer is not a beep.StreamSeeker or if seeking
// it fails.
func (r *Reverb) Seek(p int) error {
	seeker, ok := r.s.(beep.StreamSeeker)
	if !ok {
		return errors.New("effects: reverb source is not a beep.StreamSeeker")
	}
	if err := seeker.Seek(p); err != nil {
		return err
	}
	r.reset()
	return nil
}

func (r *Reverb) reset() {
	r.inLen = 0
	r.out = r.out[:0]
	r.drained = false
	r.tailLeft = 0
	r.err = nil
	for i := range r.conv {
		r.conv[i].reset()
	}
}

// produce processes one more partition of input (or tail) and appends the
// resulting samples to r.out. It reports whether any samples were produced.
func (r *Reverb) produce() bool {
	if !r.drained {
		for r.inLen < reverbPartitionSize {
			n, ok := r.s.Stream(r.in[r.inLen:])
			r.inLen += n
			if !ok {
				r.drained = true
				if err := r.s.Err(); err != nil {
					r.err = err
					return false
				}
				if r.keepTail {
					r.tailLeft = r.irLen - 1
				}
				break
			}
		}
		if !r.drained {
			// A full partition of fresh input.
			r.processBlock(r.inLen, r.inLen)
			r.inLen = 0
			return true
		}
		if r.inLen > 0 {
			// The final, partially filled partition of input. Its output also
			// contains the first part of the reverb tail, so emit as many tail
			// samples as fit into this partition as well.
			count := min(reverbPartitionSize, r.inLen+r.tailLeft)
			r.processBlock(r.inLen, count)
			r.tailLeft -= count - r.inLen
			r.inLen = 0
			return true
		}
	}
	if r.tailLeft > 0 {
		// The input has ended; flush the remaining reverb tail with silence.
		count := min(r.tailLeft, reverbPartitionSize)
		r.processBlock(0, count)
		r.tailLeft -= count
		return true
	}
	return false
}

// processBlock convolves one partition of input with the impulse response and
// appends outCount wet/dry mixed samples to r.out. The first inCount samples
// of r.in are the fresh input; the rest of the partition is silent.
func (r *Reverb) processBlock(inCount, outCount int) {
	for i := 0; i < inCount; i++ {
		r.blockL[i] = r.in[i][0]
		r.blockR[i] = r.in[i][1]
	}
	clear(r.blockL[inCount:])
	clear(r.blockR[inCount:])

	wetL := r.conv[0].process(r.blockL)
	wetR := r.conv[1].process(r.blockR)

	for i := 0; i < outCount; i++ {
		r.out = append(r.out, [2]float64{
			r.dry*r.blockL[i] + r.wet*wetL[i],
			r.dry*r.blockR[i] + r.wet*wetR[i],
		})
	}
}

// convolver is a single-channel uniformly partitioned convolver using the
// overlap-save method with a frequency-domain delay line.
type convolver struct {
	parts  [][]complex128 // impulse response partitions, in the frequency domain
	fdl    [][]complex128 // frequency-domain delay line of the input partitions
	fdlPos int            // fdl index of the most recent input partition
	prev   []float64      // previous input partition, for the overlap-save overlap
	time   []complex128   // scratch, one zero-padded partition
	acc    []complex128   // scratch, accumulated frequency-domain output
	out    []float64      // one partition of convolved output
	fft    *fftPlan
}

func newConvolver(ir []float64) convolver {
	numParts := (len(ir) + reverbPartitionSize - 1) / reverbPartitionSize
	plan := newFFTPlan(2 * reverbPartitionSize)

	c := convolver{
		parts: make([][]complex128, numParts),
		fdl:   make([][]complex128, numParts),
		prev:  make([]float64, reverbPartitionSize),
		time:  make([]complex128, plan.n),
		acc:   make([]complex128, plan.n),
		out:   make([]float64, reverbPartitionSize),
		fft:   plan,
	}

	partition := make([]complex128, plan.n)
	for k := 0; k < numParts; k++ {
		clear(partition)
		lo := k * reverbPartitionSize
		hi := min(lo+reverbPartitionSize, len(ir))
		for i := lo; i < hi; i++ {
			partition[i-lo] = complex(ir[i], 0)
		}
		plan.forward(partition)
		c.parts[k] = make([]complex128, plan.n)
		copy(c.parts[k], partition)
		c.fdl[k] = make([]complex128, plan.n)
	}
	return c
}

func (c *convolver) reset() {
	for k := range c.fdl {
		clear(c.fdl[k])
	}
	clear(c.prev)
	c.fdlPos = 0
}

// process convolves one partition of input with the impulse response and
// returns one partition of output. in must have length reverbPartitionSize.
func (c *convolver) process(in []float64) []float64 {
	n := c.fft.n
	b := reverbPartitionSize

	// Overlap-save: transform the previous and the current partition together.
	for i := 0; i < b; i++ {
		c.time[i] = complex(c.prev[i], 0)
		c.time[b+i] = complex(in[i], 0)
	}
	copy(c.prev, in)
	c.fft.forward(c.time)

	// Push the transform into the frequency-domain delay line.
	c.fdlPos = (c.fdlPos + len(c.fdl) - 1) % len(c.fdl)
	copy(c.fdl[c.fdlPos], c.time)

	// Accumulate the convolutions with all impulse response partitions.
	clear(c.acc)
	for k := 0; k < len(c.parts); k++ {
		x := c.fdl[(c.fdlPos+k)%len(c.fdl)]
		h := c.parts[k]
		for i := 0; i < n; i++ {
			c.acc[i] += x[i] * h[i]
		}
	}

	c.fft.inverse(c.acc)

	// The first half of the inverse transform is corrupted by circular
	// convolution aliasing; the second half is the valid output.
	for i := 0; i < b; i++ {
		c.out[i] = real(c.acc[b+i])
	}
	return c.out
}

// fftPlan is a radix-2 Cooley-Tukey FFT plan for a fixed transform size.
type fftPlan struct {
	n        int
	rev      []int        // bit-reversal permutation
	roots    []complex128 // twiddle factors exp(-2*pi*i*k/n), k < n/2
	rootsInv []complex128 // conjugated twiddle factors for the inverse transform
}

func newFFTPlan(n int) *fftPlan {
	if n < 2 || n&(n-1) != 0 {
		panic(fmt.Sprintf("effects: FFT size %d is not a power of two", n))
	}
	levels := bits.TrailingZeros(uint(n))
	rev := make([]int, n)
	for i := 1; i < n; i++ {
		rev[i] = rev[i>>1]>>1 | (i&1)<<(levels-1)
	}
	roots := make([]complex128, n/2)
	rootsInv := make([]complex128, n/2)
	for k := range roots {
		angle := -2 * math.Pi * float64(k) / float64(n)
		roots[k] = complex(math.Cos(angle), math.Sin(angle))
		rootsInv[k] = complex(math.Cos(angle), -math.Sin(angle))
	}
	return &fftPlan{n: n, rev: rev, roots: roots, rootsInv: rootsInv}
}

func (p *fftPlan) forward(x []complex128) {
	p.transform(x, p.roots)
}

// inverse computes the inverse transform, including the 1/n normalization.
func (p *fftPlan) inverse(x []complex128) {
	p.transform(x, p.rootsInv)
	scale := complex(1/float64(p.n), 0)
	for i := range x {
		x[i] *= scale
	}
}

func (p *fftPlan) transform(x []complex128, roots []complex128) {
	for i, j := range p.rev {
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for size := 2; size <= p.n; size <<= 1 {
		half := size / 2
		step := p.n / size
		for i := 0; i < p.n; i += size {
			for j := 0; j < half; j++ {
				tw := roots[j*step]
				u := x[i+j]
				v := x[i+j+half] * tw
				x[i+j] = u + v
				x[i+j+half] = u - v
			}
		}
	}
}
