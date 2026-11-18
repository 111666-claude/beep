package loudness

import (
	"errors"
	"math"

	"github.com/gopxl/beep/v2"
)

const (
	// blockDuration is the BS.1770-4 gating-block interval: 100 ms, no overlap.
	blockDuration = 0.1

	// momentaryBlocks / shortTermBlocks are the window lengths expressed in
	// 100 ms blocks (400 ms and 3 s).
	momentaryBlocks = 4
	shortTermBlocks = 30

	// absGateLUFS is the BS.1770-4 absolute gating threshold of -70 LUFS.
	absGateLUFS = -70.0

	// relativeGateLU is the BS.1770-4 relative gating offset of -10 LU below
	// the ungated absolute-gated mean.
	relativeGateLU = -10.0

	// Histogram edges in LUFS. The lower edge equals the absolute gate, so a
	// block at exactly -70 LUFS lands in the first bin; everything below it
	// (including -Inf silence) is discarded, everything above histMaxLUFS is
	// clamped to the last bin.
	histBinWidth = 0.1
	histMinLUFS  = absGateLUFS
	histMaxLUFS  = 10.0
	histBins     = 800
)

// K-weighting analog prototype parameters (libebur128 / ITU-R BS.1770-4).
const (
	shelfF0 = 1681.974450955533
	gainDB  = 3.9997152707983712
	shelfQ  = 0.7071752369554196

	hpF0 = 38.13547087602444
	hpQ  = 0.5003270373238773
)

// SilenceLUFS is returned by the loudness accessors when no complete,
// non-silent gating block is available yet (for example before 400 ms of
// audio have been streamed, on an all-silent stream, or right after Reset).
var SilenceLUFS = math.Inf(-1)

// biquad is a transposed direct-form-II biquad filter. Coefficients use the
// convention y = b0*x + b1*x1 + b2*x2 - a1*y1 - a2*y2, where a1/a2 are the
// normalized denominator coefficients.
type biquad struct {
	b0, b1, b2, a1, a2 float64
	x1, x2, y1, y2     float64
}

func (f *biquad) process(x float64) float64 {
	y := f.b0*x + f.b1*f.x1 + f.b2*f.x2 - f.a1*f.y1 - f.a2*f.y2
	f.x2, f.x1 = f.x1, x
	f.y2, f.y1 = f.y1, y
	return y
}

func (f *biquad) reset() {
	f.x1, f.x2, f.y1, f.y2 = 0, 0, 0, 0
}

// kWeight builds the two K-weighting stages (a high-shelf and a high-pass)
// for the given sample rate. The coefficients are derived from the analog
// prototype filters in ITU-R BS.1770-4 with bilinear transform and frequency
// pre-warping, following the reference implementation in libebur128.
func kWeight(sampleRate int) (shelf, highpass *biquad) {
	// High-shelf filter: f0 = 1681.974450955533 Hz, gain = +3.9997... dB,
	// Q = 0.7071752369554196. Coefficients follow the libebur128 reference
	// implementation of ITU-R BS.1770-4.
	k := math.Tan(math.Pi * shelfF0 / float64(sampleRate))
	vg := math.Pow(10.0, gainDB/20.0)
	va := 1.0 / shelfQ
	vb := math.Sqrt(vg) / shelfQ
	den := 1.0 + va*k + k*k
	shelf = &biquad{
		b0: (vg + vb*k + k*k) / den,
		b1: 2.0 * (k*k - vg) / den,
		b2: (vg - vb*k + k*k) / den,
		a1: 2.0 * (k*k - 1.0) / den,
		a2: (1.0 - va*k + k*k) / den,
	}

	// High-pass filter.
	k = math.Tan(math.Pi * hpF0 / float64(sampleRate))
	va = 1.0 / hpQ
	den = 1.0 + va*k + k*k
	a2 := 1.0 - va*k + k*k
	highpass = &biquad{
		b0: 1.0 / den,
		b1: -2.0 / den,
		b2: 1.0 / den,
		a1: 2.0 * (k*k - 1.0) / den,
		a2: a2 / den,
	}
	return shelf, highpass
}

// Meter wraps a Streamer, passes all samples through verbatim and measures the
// loudness of everything that flows through it according to ITU-R BS.1770-4.
//
// The zero value is not usable; create one with NewMeter.
type Meter struct {
	streamer beep.Streamer

	// K-weighting filters, one chain per channel.
	shelfL, highpassL biquad
	shelfR, highpassR biquad

	// Samples per 100 ms gating block.
	blockLen int
	// Samples accumulated into the block currently being filled.
	blockPos int
	// Running per-channel sum of K-weighted squared samples of the current
	// block.
	blockSumL, blockSumR float64

	// Ring buffers of completed block mean-square values (z_j, both channels
	// weighted 1.0). Fixed size, so window memory is bounded.
	ring       [shortTermBlocks]float64
	ringPos    int
	ringFilled int

	// Integrated-loudness histogram. Only blocks above the -70 LUFS absolute
	// gate are stored; the number of bins is fixed, so integrated-loudness
	// memory is bounded independently of stream length.
	histCount [histBins]int64
	histSum   [histBins]float64
}

// NewMeter wraps s and measures loudness at the given sample rate. The sample
// rate must be positive and above twice the ~1.7 kHz K-weighting shelf
// frequency (every practical audio rate qualifies); otherwise an error is
// returned and no Streamer is wrapped.
func NewMeter(sr beep.SampleRate, s beep.Streamer) (*Meter, error) {
	if sr <= 0 {
		return nil, errors.New("loudness: sample rate must be positive")
	}
	if s == nil {
		return nil, errors.New("loudness: wrapped streamer must not be nil")
	}
	if float64(sr) <= 2.0*shelfF0 {
		return nil, errors.New("loudness: sample rate must exceed twice the K-weighting shelf frequency (3364 Hz)")
	}
	shelf, highpass := kWeight(int(sr))
	blockLen := int(math.Round(float64(sr) * blockDuration))
	if blockLen < 1 {
		return nil, errors.New("loudness: sample rate too low to form a 100 ms gating block")
	}
	return &Meter{
		streamer:  s,
		shelfL:    *shelf,
		highpassL: *highpass,
		shelfR:    *shelf,
		highpassR: *highpass,
		blockLen:  blockLen,
	}, nil
}

// Stream streams from the wrapped Streamer without modifying any sample and
// feeds an internal copy of the samples through the K-weighting and gating
// stages. The returned n, ok and the contents of samples[:n] are exactly those
// produced by the wrapped Streamer.
func (m *Meter) Stream(samples [][2]float64) (n int, ok bool) {
	n, ok = m.streamer.Stream(samples)
	for i := 0; i < n; i++ {
		// Read-only: the output slice is never written to.
		l := samples[i][0]
		r := samples[i][1]

		l = m.highpassL.process(m.shelfL.process(l))
		r = m.highpassR.process(m.shelfR.process(r))

		m.blockSumL += l * l
		m.blockSumR += r * r
		m.blockPos++

		if m.blockPos == m.blockLen {
			m.commitBlock()
		}
	}
	return n, ok
}

// commitBlock finalizes the current 100 ms gating block.
func (m *Meter) commitBlock() {
	// Stereo/mono handling: every beep Streamer carries two channels and both
	// use the BS.1770 channel weight 1.0. Mono sources are simply identical
	// channels, which yields the correct -3.01 LUFS mono offset naturally.
	z := (m.blockSumL + m.blockSumR) / float64(m.blockLen)

	// Replace the oldest block in the fixed-size ring buffer.
	m.ring[m.ringPos] = z
	m.ringPos = (m.ringPos + 1) % shortTermBlocks
	if m.ringFilled < shortTermBlocks {
		m.ringFilled++
	}

	// Record the block for integrated loudness when it passes the absolute
	// gate. Silence blocks (z == 0) yield -Inf LUFS and are discarded.
	if z > 0.0 && lufs(z) >= histMinLUFS {
		bin := int((lufs(z) - histMinLUFS) / histBinWidth)
		if bin < 0 {
			bin = 0
		} else if bin >= histBins {
			bin = histBins - 1
		}
		m.histCount[bin]++
		m.histSum[bin] += z
	}

	m.blockPos = 0
	m.blockSumL = 0.0
	m.blockSumR = 0.0
}

// ringMean returns the mean block energy over the last count blocks, or false
// when fewer than count complete blocks are available.
func (m *Meter) ringMean(count int) (float64, bool) {
	if m.ringFilled < count {
		return 0, false
	}
	sum := 0.0
	// ringFilled >= count implies the ring holds the last ringFilled blocks;
	// the newest block sits at ringPos-1.
	start := (m.ringPos - count + shortTermBlocks) % shortTermBlocks
	for i := 0; i < count; i++ {
		sum += m.ring[(start+i)%shortTermBlocks]
	}
	return sum / float64(count), true
}

// available reports whether a window with the given mean K-weighted energy is
// reportable under BS.1770-4: a window whose loudness does not exceed the
// -70 LUFS absolute gate is "not available" (the filter's decaying response
// on silence would otherwise produce arbitrarily small finite values).
func available(meanSquare float64) bool {
	return meanSquare > 0.0 && lufs(meanSquare) > absGateLUFS
}

// Momentary returns the momentary loudness in LUFS, computed over the most
// recent 400 ms window. It returns SilenceLUFS until at least 400 ms of audio
// have been streamed, or when the window is at or below the -70 LUFS absolute
// gate (for example a fully silent window).
func (m *Meter) Momentary() float64 {
	mean, ok := m.ringMean(momentaryBlocks)
	if !ok || !available(mean) {
		return SilenceLUFS
	}
	return lufs(mean)
}

// ShortTerm returns the short-term loudness in LUFS, computed over the most
// recent 3 s window. It returns SilenceLUFS until at least 3 s of audio have
// been streamed, or when the window is at or below the -70 LUFS absolute
// gate (for example a fully silent window).
func (m *Meter) ShortTerm() float64 {
	mean, ok := m.ringMean(shortTermBlocks)
	if !ok || !available(mean) {
		return SilenceLUFS
	}
	return lufs(mean)
}

// Integrated returns the integrated (whole-stream, dual-gated) loudness in
// LUFS according to BS.1770-4: blocks below the -70 LUFS absolute gate and
// blocks more than 10 LU below the ungated absolute-gated mean are excluded.
//
// It returns SilenceLUFS when no block passes the absolute gate (an empty or
// all-silent stream).
func (m *Meter) Integrated() float64 {
	var totalCount int64
	var totalSum float64
	for bin := range m.histCount {
		totalCount += m.histCount[bin]
		totalSum += m.histSum[bin]
	}
	if totalCount == 0 {
		return SilenceLUFS
	}

	// Ungated mean over all absolute-gated blocks; the relative gate sits
	// 10 LU below it.
	relativeThreshold := lufs(totalSum/float64(totalCount)) + relativeGateLU

	var gatedCount int64
	var gatedSum float64
	for bin := range m.histCount {
		if m.histCount[bin] == 0 {
			continue
		}
		// Lower edge of the bin in LUFS; the whole bin is included only when
		// even its quietest block clears the relative threshold.
		binLow := histMinLUFS + float64(bin)*histBinWidth
		if binLow >= relativeThreshold {
			gatedCount += m.histCount[bin]
			gatedSum += m.histSum[bin]
		}
	}
	// The relative threshold can never exceed the ungated mean by more than
	// its 10 LU offset, so at least the loudest blocks always survive; guard
	// against the edge anyway.
	if gatedCount == 0 {
		return SilenceLUFS
	}
	return lufs(gatedSum / float64(gatedCount))
}

// Reset clears all measurement state, including the K-weighting filter
// histories, as if no samples had ever been streamed. The wrapped Streamer is
// not touched. Subsequent measurements start from the next streamed sample.
func (m *Meter) Reset() {
	m.shelfL.reset()
	m.highpassL.reset()
	m.shelfR.reset()
	m.highpassR.reset()

	m.blockPos = 0
	m.blockSumL = 0.0
	m.blockSumR = 0.0

	m.ringPos = 0
	m.ringFilled = 0
	for i := range m.ring {
		m.ring[i] = 0
	}
	for i := range m.histCount {
		m.histCount[i] = 0
		m.histSum[i] = 0
	}
}

// Err propagates the wrapped Streamer's errors.
func (m *Meter) Err() error {
	return m.streamer.Err()
}

// lufs converts a mean K-weighted energy into LUFS. The -0.691 dB offset is
// the monophonic K-weighting normalization constant from ITU-R BS.1770-4.
func lufs(meanSquare float64) float64 {
	return -0.691 + 10.0*math.Log10(meanSquare)
}
