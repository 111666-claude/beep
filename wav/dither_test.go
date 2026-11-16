package wav

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"testing"

	"github.com/orcaman/writerseeker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/generators"
	"github.com/gopxl/beep/v2/internal/testtools"
)

// sineSamples returns a fixed window of a sine tone, used as a deterministic
// test signal.
func sineSamples(t *testing.T, num int, freq float64) [][2]float64 {
	t.Helper()
	s, err := generators.SineTone(44100, freq)
	require.NoError(t, err)
	return testtools.CollectNum(num, s)
}

// fadeSamples returns a quiet signal fading from 1/4 LSB to nothing. Without
// dither it collapses to silence after the first steps; with dither it should
// retain variation all the way down.
func fadeSamples(num int, precision int) [][2]float64 {
	lsb := 1.0 / math.Exp2(float64(precision*8-1))
	samples := make([][2]float64, num)
	for i := range samples {
		x := lsb * (1 - float64(i)/float64(num))
		samples[i] = [2]float64{x, x}
	}
	return samples
}

// chunkedStreamer streams fixed data in chunks of at most max samples, which
// lets tests exercise any chunk size independently of the encoder's internal
// buffer.
type chunkedStreamer struct {
	data [][2]float64
	max  int
	pos  int
}

func newChunkedStreamer(data [][2]float64, max int) *chunkedStreamer {
	return &chunkedStreamer{data: data, max: max}
}

func (s *chunkedStreamer) Stream(samples [][2]float64) (n int, ok bool) {
	if s.pos >= len(s.data) {
		return 0, false
	}
	n = copy(samples[:min(len(samples), s.max)], s.data[s.pos:])
	s.pos += n
	return n, true
}

func (s *chunkedStreamer) Err() error { return nil }

func writerBytes(t *testing.T, w *writerseeker.WriterSeeker) []byte {
	t.Helper()
	b, err := io.ReadAll(w.Reader())
	require.NoError(t, err)
	return b
}

func encodeBytes(t *testing.T, s beep.Streamer, format beep.Format, options *EncodeOptions) []byte {
	t.Helper()
	var w writerseeker.WriterSeeker
	var err error
	if options == nil {
		err = Encode(&w, s, format)
	} else {
		err = EncodeWithOptions(&w, s, format, *options)
	}
	require.NoError(t, err)
	return writerBytes(t, &w)
}

// wavData parses the header fields of an encoded WAV file and returns its
// payload.
func wavData(t *testing.T, encoded []byte) (fileSize, dataSize int32, data []byte) {
	t.Helper()
	require.GreaterOrEqual(t, len(encoded), 44)
	require.Equal(t, "WAVE", string(encoded[8:12]))
	require.Equal(t, "fmt ", string(encoded[12:16]))
	require.Equal(t, "data", string(encoded[36:40]))
	fileSize = int32(binary.LittleEndian.Uint32(encoded[4:8]))
	dataSize = int32(binary.LittleEndian.Uint32(encoded[40:44]))
	return fileSize, dataSize, encoded[44:]
}

func TestEncodeDitherDefaultsToUnchangedBytes(t *testing.T) {
	format := beep.Format{SampleRate: 44100, NumChannels: 2, Precision: 2}
	samples := sineSamples(t, 1000, 440)

	plain := encodeBytes(t, newChunkedStreamer(samples, 512), format, nil)
	explicit := encodeBytes(t, newChunkedStreamer(samples, 512), format, &EncodeOptions{})
	withWrapper := func() []byte {
		var w writerseeker.WriterSeeker
		require.NoError(t, EncodeWithOptions(&w, newChunkedStreamer(samples, 512), format, EncodeOptions{Dither: false, Seed: 12345}))
		return writerBytes(t, &w)
	}()

	assert.Equal(t, plain, explicit, "EncodeWithOptions without dither must match Encode")
	assert.Equal(t, plain, withWrapper, "a seed without Dither must not change the output")

	// Sanity check: the dithered output differs from the undithered one.
	dithered := encodeBytes(t, newChunkedStreamer(samples, 512), format, &EncodeOptions{Dither: true, Seed: 1})
	assert.NotEqual(t, plain, dithered)
	assert.Equal(t, len(plain), len(dithered), "dither must not change the file length")
}

func TestEncodeDitherChunkSizeIndependence(t *testing.T) {
	precisions := []int{2, 3}
	signals := map[string][][2]float64{
		"sine": sineSamples(t, 2000, 440),
		"fade": fadeSamples(2000, 2),
	}

	for _, precision := range precisions {
		for name, samples := range signals {
			t.Run(fmt.Sprintf("%s_%d_bit", name, precision*8), func(t *testing.T) {
				format := beep.Format{SampleRate: 44100, NumChannels: 2, Precision: precision}
				options := EncodeOptions{Dither: true, Seed: 0x5eed}

				reference := encodeBytes(t, newChunkedStreamer(samples, len(samples)), format, &options)
				for _, chunk := range []int{1, 2, 3, 7, 64, 511, 512, 513, 1000, len(samples)} {
					got := encodeBytes(t, newChunkedStreamer(samples, chunk), format, &options)
					assert.Equal(t, reference, got, "output differs for chunk size %d", chunk)
				}
			})
		}
	}
}

func TestEncodeDitherDeterminism(t *testing.T) {
	format := beep.Format{SampleRate: 48000, NumChannels: 2, Precision: 2}
	samples := sineSamples(t, 500, 1000)

	first := encodeBytes(t, newChunkedStreamer(samples, 100), format, &EncodeOptions{Dither: true, Seed: 42})
	second := encodeBytes(t, newChunkedStreamer(samples, 100), format, &EncodeOptions{Dither: true, Seed: 42})
	assert.Equal(t, first, second, "same input and seed must produce identical bytes")

	other := encodeBytes(t, newChunkedStreamer(samples, 100), format, &EncodeOptions{Dither: true, Seed: 43})
	assert.NotEqual(t, first, other, "different seeds should produce different noise")

	// The convenience wrapper must agree with the options form.
	var w writerseeker.WriterSeeker
	require.NoError(t, EncodeWithDither(&w, newChunkedStreamer(samples, 100), format, 42))
	assert.Equal(t, first, writerBytes(t, &w))
}

func TestEncodeDitherSaturation(t *testing.T) {
	tests := []struct {
		precision int
		sample    [2]float64
		want      []byte // little endian, one channel
	}{
		{1, [2]float64{1, 1}, []byte{0xff}},
		{1, [2]float64{-1, -1}, []byte{0x00}},
		{1, [2]float64{2, 2}, []byte{0xff}},
		{1, [2]float64{-2, -2}, []byte{0x00}},
		{2, [2]float64{1, 1}, []byte{0xff, 0x7f}},
		{2, [2]float64{-1, -1}, []byte{0x00, 0x80}},
		{2, [2]float64{1.5, 1.5}, []byte{0xff, 0x7f}},
		{2, [2]float64{-1.5, -1.5}, []byte{0x00, 0x80}},
		{3, [2]float64{1, 1}, []byte{0xff, 0xff, 0x7f}},
		{3, [2]float64{-1, -1}, []byte{0x00, 0x00, 0x80}},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("%d_bit_%v", test.precision*8, test.sample), func(t *testing.T) {
			format := beep.Format{SampleRate: 44100, NumChannels: 1, Precision: test.precision}

			// Check several seeds: saturation must not depend on the noise.
			for seed := int64(-2); seed <= 2; seed++ {
				s := testtools.NewDataStreamer([][2]float64{test.sample})
				encoded := encodeBytes(t, s, format, &EncodeOptions{Dither: true, Seed: seed})
				// A mono data streamer gets mixed down by beep.Format, but both
				// channels are equal, so the result is the sample itself.
				_, _, data := wavData(t, encoded)
				require.Len(t, data, test.precision)
				assert.Equal(t, test.want, data, "seed %d", seed)
			}
		})
	}
}

// decodeDithered decodes the payload of an encoded file into integer sample
// values (one value per channel per frame).
func decodeDithered(t *testing.T, encoded []byte, precision int) []int64 {
	t.Helper()
	_, _, data := wavData(t, encoded)
	require.Zero(t, len(data)%precision)
	values := make([]int64, 0, len(data)/precision)
	for i := 0; i < len(data); i += precision {
		var u uint64
		for b := precision - 1; b >= 0; b-- {
			u = u<<8 | uint64(data[i+b])
		}
		if precision == 1 {
			values = append(values, int64(u)-128)
			continue
		}
		bits := uint(precision * 8)
		values = append(values, int64(u<<(64-bits))>>(64-bits))
	}
	return values
}

func TestEncodeDitherTPDFDistribution(t *testing.T) {
	const (
		precision = 2
		frames    = 40000
	)
	format := beep.Format{SampleRate: 44100, NumChannels: 1, Precision: precision}
	samples := make([][2]float64, frames) // exact digital silence

	encoded := encodeBytes(t, newChunkedStreamer(samples, 333), format, &EncodeOptions{Dither: true, Seed: 2024})
	values := decodeDithered(t, encoded, precision)
	require.Len(t, values, frames)

	var counts [3]int // -1, 0, +1
	var sum, sumSquares float64
	for _, v := range values {
		switch {
		case v < -1 || v > 1:
			t.Fatalf("null sample quantized outside {-1, 0, +1} LSB: %d", v)
		case v == -1:
			counts[0]++
		case v == 0:
			counts[1]++
		default:
			counts[2]++
		}
		sum += float64(v)
		sumSquares += float64(v * v)
	}

	// For TPDF noise spanning two LSB peak to peak, a null sample rounds to
	// -1, 0 or +1 with probabilities 1/8, 3/4 and 1/8 respectively.
	const tolerance = 0.02
	assert.InDelta(t, 0.125, float64(counts[0])/frames, tolerance)
	assert.InDelta(t, 0.75, float64(counts[1])/frames, tolerance)
	assert.InDelta(t, 0.125, float64(counts[2])/frames, tolerance)

	// For a null sample the rounded output is ±1 with probability 1/8 each, so
	// the quantized noise is zero-mean with variance 2 * 1/8 = 0.25 LSB².
	mean := sum / frames
	variance := sumSquares/frames - mean*mean
	assert.InDelta(t, 0, mean, 0.02)
	assert.InDelta(t, 0.25, variance, 0.03)
}

func TestEncodeDitherBreaksUpQuantizationBanding(t *testing.T) {
	// A very quiet fade, entirely below half an LSB at the start and fading to
	// zero, is the textbook case that produces audible quantization grain.
	const (
		precision = 2
		frames    = 5000
	)
	format := beep.Format{SampleRate: 44100, NumChannels: 2, Precision: precision}
	samples := fadeSamples(frames, precision)

	plain := encodeBytes(t, newChunkedStreamer(samples, 512), format, nil)
	plainValues := decodeDithered(t, plain, precision)
	plainLevels := map[int64]struct{}{}
	for _, v := range plainValues {
		plainLevels[v] = struct{}{}
	}

	dithered := encodeBytes(t, newChunkedStreamer(samples, 512), format, &EncodeOptions{Dither: true, Seed: 7})
	ditherValues := decodeDithered(t, dithered, precision)
	ditherLevels := map[int64]struct{}{}
	for _, v := range ditherValues {
		ditherLevels[v] = struct{}{}
	}

	assert.Less(t, len(plainLevels), 5, "without dither the quiet fade collapses into a few levels")
	assert.Greater(t, len(ditherLevels), len(plainLevels), "dither should spread the quiet fade over more levels")

	// Every dithered sample stays within one and a half LSB of the ideal
	// position: up to half an LSB of rounding plus up to one LSB of noise.
	// The decoded values are interleaved left and right; both channels carry
	// the same fade, so the frame index is i/2.
	for i, v := range ditherValues {
		ideal := samples[i/2][0] * math.Exp2(float64(precision*8-1))
		if math.Abs(float64(v)-ideal) > 1.5 {
			t.Fatalf("sample %d deviates by more than 1.5 LSB: %d vs %.3f", i, v, ideal)
		}
	}
}

func TestEncodeErrorInvalidPrecision(t *testing.T) {
	var w writerseeker.WriterSeeker
	format := beep.Format{SampleRate: 44100, NumChannels: 2, Precision: 4}

	err := Encode(&w, generators.Silence(10), format)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wav")
	assert.Contains(t, err.Error(), "unsupported precision")

	err = EncodeWithOptions(&w, generators.Silence(10), format, EncodeOptions{Dither: true, Seed: 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported precision")

	assert.Empty(t, writerBytes(t, &w), "nothing must be written for an invalid format")
}

func TestEncodeErrorNonFiniteSample(t *testing.T) {
	for name, bad := range map[string]float64{
		"NaN":  math.NaN(),
		"+Inf": math.Inf(1),
		"-Inf": math.Inf(-1),
	} {
		t.Run(name, func(t *testing.T) {
			data := [][2]float64{{0, 0}, {0.5, bad}}
			s := testtools.NewDataStreamer(data)
			format := beep.Format{SampleRate: 44100, NumChannels: 2, Precision: 2}

			var w writerseeker.WriterSeeker
			err := EncodeWithOptions(&w, s, format, EncodeOptions{Dither: true, Seed: 3})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "non-finite sample")

			// The partially written file must still report consistent sizes.
			fileSize, dataSize, payload := wavData(t, writerBytes(t, &w))
			assert.Equal(t, dataSize, int32(len(payload)))
			assert.Equal(t, int32(44-8)+dataSize, fileSize)
			assert.Zero(t, dataSize, "the frames before the bad one must not be counted")
		})
	}
}

type errorStreamer struct {
	streamed int
	err      error
}

func (s *errorStreamer) Stream(samples [][2]float64) (n int, ok bool) {
	if s.streamed >= 100 {
		return 0, false
	}
	n = min(len(samples), 100-s.streamed)
	for i := 0; i < n; i++ {
		samples[i] = [2]float64{0.1, -0.1}
	}
	s.streamed += n
	return n, true
}

func (s *errorStreamer) Err() error { return s.err }

func TestEncodeErrorStreamerError(t *testing.T) {
	format := beep.Format{SampleRate: 44100, NumChannels: 2, Precision: 2}
	var w writerseeker.WriterSeeker

	underlying := errors.New("decoder exploded")
	err := EncodeWithOptions(&w, &errorStreamer{err: underlying}, format, EncodeOptions{Dither: true, Seed: 9})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wav")
	assert.Contains(t, err.Error(), "streamer")
	assert.Contains(t, err.Error(), "decoder exploded")

	// The file must have a valid, consistent header describing the data which
	// was actually written.
	fileSize, dataSize, payload := wavData(t, writerBytes(t, &w))
	assert.Equal(t, dataSize, int32(len(payload)))
	assert.Equal(t, int32(44-8)+dataSize, fileSize)
	assert.Equal(t, int32(100*format.Width()), dataSize)
}

// failingWriterSeeker fails every write after limit bytes have been accepted.
type failingWriterSeeker struct {
	inner writerseeker.WriterSeeker
	limit int
	err   error
}

func (w *failingWriterSeeker) Write(p []byte) (int, error) {
	if w.limit <= 0 {
		return 0, w.err
	}
	if len(p) > w.limit {
		n, _ := w.inner.Write(p[:w.limit])
		w.limit = 0
		return n, w.err
	}
	n, err := w.inner.Write(p)
	w.limit -= n
	return n, err
}

func (w *failingWriterSeeker) Seek(offset int64, whence int) (int64, error) {
	return w.inner.Seek(offset, whence)
}

func TestEncodeErrorWriteFailure(t *testing.T) {
	format := beep.Format{SampleRate: 44100, NumChannels: 2, Precision: 3}
	samples := sineSamples(t, 3000, 200)
	writeErr := errors.New("disk full")

	for _, limit := range []int{0, 10, 44, 45, 100, 1000, 5000, 10000} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			w := &failingWriterSeeker{limit: limit, err: writeErr}
			err := EncodeWithOptions(w, newChunkedStreamer(samples, 128), format, EncodeOptions{Dither: true, Seed: 1})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "disk full")

			// Whatever reached the writer must form a WAV whose declared
			// lengths match the bytes actually present.
			written := writerBytes(t, &w.inner)
			if len(written) < 44 {
				return
			}
			fileSize, dataSize, payload := wavData(t, written)
			// The header is repaired on a best-effort basis. A writer which
			// refuses every subsequent byte may leave the empty placeholder
			// sizes in place, so the declared sizes must never exceed what was
			// actually written, and the two fields must stay consistent.
			assert.LessOrEqual(t, dataSize, int32(len(payload)), "declared data size must not exceed the payload")
			assert.LessOrEqual(t, fileSize, int32(len(written)-8), "declared file size must not exceed the file")
			assert.Equal(t, int32(44-8)+dataSize, fileSize)
		})
	}
}

func TestEncodeOptionsMultipleValuesRejected(t *testing.T) {
	var w writerseeker.WriterSeeker
	format := beep.Format{SampleRate: 44100, NumChannels: 2, Precision: 2}
	err := Encode(&w, generators.Silence(1), format, EncodeOptions{}, EncodeOptions{})
	require.Error(t, err)
	assert.Empty(t, writerBytes(t, &w))
}

func TestEncodeDither8Bit(t *testing.T) {
	format := beep.Format{SampleRate: 22050, NumChannels: 2, Precision: 1}
	samples := sineSamples(t, 1000, 300)

	reference := encodeBytes(t, newChunkedStreamer(samples, 1000), format, &EncodeOptions{Dither: true, Seed: 5})
	for _, chunk := range []int{1, 17, 512} {
		assert.Equal(t, reference, encodeBytes(t, newChunkedStreamer(samples, chunk), format, &EncodeOptions{Dither: true, Seed: 5}))
	}

	// The output remains valid unsigned 8-bit PCM.
	_, dataSize, payload := wavData(t, reference)
	assert.Equal(t, int32(len(payload)), dataSize)

	// Round trip through the decoder, which must stay within one LSB.
	s, decodedFormat, err := Decode(bytes.NewReader(reference))
	require.NoError(t, err)
	assert.Equal(t, format, decodedFormat)
	decoded := testtools.Collect(s)
	require.Len(t, decoded, len(samples))
	lsb := 2.0 / 256.0
	for i := range decoded {
		for c := 0; c < 2; c++ {
			assert.InDelta(t, samples[i][c], decoded[i][c], 1.5*lsb)
		}
	}
}

func TestEncodeDither24BitRoundTrip(t *testing.T) {
	format := beep.Format{SampleRate: 96000, NumChannels: 2, Precision: 3}
	samples := sineSamples(t, 1000, 440)

	encoded := encodeBytes(t, newChunkedStreamer(samples, 37), format, &EncodeOptions{Dither: true, Seed: 11})
	s, decodedFormat, err := Decode(bytes.NewReader(encoded))
	require.NoError(t, err)
	assert.Equal(t, format, decodedFormat)

	decoded := testtools.Collect(s)
	require.Len(t, decoded, len(samples))
	lsb := 2.0 / math.Exp2(24)
	for i := range decoded {
		for c := 0; c < 2; c++ {
			assert.InDelta(t, samples[i][c], decoded[i][c], 1.5*lsb)
		}
	}
}

// stuckStreamer always reports more samples without producing any, which would
// make a naive encoder loop forever.
type stuckStreamer struct{}

func (stuckStreamer) Stream([][2]float64) (int, bool) { return 0, true }
func (stuckStreamer) Err() error                      { return nil }

func TestEncodeErrorStreamerNoProgress(t *testing.T) {
	format := beep.Format{SampleRate: 44100, NumChannels: 2, Precision: 2}
	var w writerseeker.WriterSeeker

	err := Encode(&w, stuckStreamer{}, format)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no samples without being drained")

	fileSize, dataSize, payload := wavData(t, writerBytes(t, &w))
	assert.Zero(t, dataSize)
	assert.Zero(t, len(payload))
	assert.Equal(t, int32(44-8), fileSize)
}

var _ beep.Streamer = (*chunkedStreamer)(nil)
var _ beep.Streamer = (*errorStreamer)(nil)
var _ io.WriteSeeker = (*failingWriterSeeker)(nil)

func TestEncodeDitherGoldenBytes(t *testing.T) {
	// Fixed input: digital silence, quarter scale, half scale and both full
	// scale rails. The expected payloads pin the exact noise sequence for seed
	// 1, so accidental changes to the generator or the quantization are caught.
	data := [][2]float64{{0, 0}, {0.25, -0.25}, {0.5, -0.5}, {1, -1}}

	golden := map[int][]byte{
		1: {
			0x80, 0x81, 0xa0, 0x60, 0xbf, 0x40, 0xff, 0x00,
		},
		2: {
			0x00, 0x00, 0x01, 0x00, 0x00, 0x20, 0x00, 0xe0,
			0xff, 0x3f, 0x00, 0xc0, 0xff, 0x7f, 0x00, 0x80,
		},
		3: {
			0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00,
			0x20, 0x00, 0x00, 0xe0, 0xff, 0xff, 0x3f, 0x00,
			0x00, 0xc0, 0xff, 0xff, 0x7f, 0x00, 0x00, 0x80,
		},
	}

	for precision, want := range golden {
		t.Run(fmt.Sprintf("%d_bit", precision*8), func(t *testing.T) {
			format := beep.Format{SampleRate: 44100, NumChannels: 2, Precision: precision}
			encoded := encodeBytes(t, newChunkedStreamer(data, 2), format, &EncodeOptions{Dither: true, Seed: 1})
			_, dataSize, payload := wavData(t, encoded)
			assert.Equal(t, int32(len(want)), dataSize)
			assert.Equal(t, want, payload)
		})
	}
}

func TestEncodeDitherDecorrelatesHalfLSBOffset(t *testing.T) {
	// A DC signal exactly halfway between two quantization levels rounds the
	// same way every time without dither, producing a pure tone at the
	// quantization error. With TPDF dither the output must alternate between
	// the two neighbouring levels.
	const (
		precision = 2
		frames    = 20000
	)
	scale := math.Exp2(float64(precision*8 - 1))
	x := 0.5 / scale

	format := beep.Format{SampleRate: 44100, NumChannels: 1, Precision: precision}
	plainSamples := make([][2]float64, frames)
	ditheredSamples := make([][2]float64, frames)
	for i := range plainSamples {
		plainSamples[i] = [2]float64{x, x}
		ditheredSamples[i] = [2]float64{x, x}
	}

	plain := decodeDithered(t, encodeBytes(t, newChunkedStreamer(plainSamples, 512), format, nil), precision)
	dithered := decodeDithered(t, encodeBytes(t, newChunkedStreamer(ditheredSamples, 512), format, &EncodeOptions{Dither: true, Seed: 77}), precision)

	plainLevels := map[int64]int{}
	for _, v := range plain {
		plainLevels[v]++
	}
	assert.Len(t, plainLevels, 1, "without dither the half-LSB DC offset always rounds to the same value")

	zero, one := 0, 0
	for _, v := range dithered {
		switch v {
		case 0:
			zero++
		case 1:
			one++
		default:
			t.Fatalf("unexpected level %d, want 0 or 1", v)
		}
	}
	// Rounding a uniform quantization error yields both neighboring levels
	// with roughly equal probability.
	assert.InDelta(t, 0.5, float64(zero)/frames, 0.03)
	assert.InDelta(t, 0.5, float64(one)/frames, 0.03)
}

func TestEncodeDitherChannelsAreIndependent(t *testing.T) {
	// Both channels carry identical silence, so any difference in the encoded
	// values must come from per-channel noise.
	const (
		precision = 2
		frames    = 5000
	)
	format := beep.Format{SampleRate: 44100, NumChannels: 2, Precision: precision}
	samples := make([][2]float64, frames)
	encoded := encodeBytes(t, newChunkedStreamer(samples, 250), format, &EncodeOptions{Dither: true, Seed: 99})
	values := decodeDithered(t, encoded, precision)
	require.Len(t, values, frames*2)

	different := 0
	for i := 0; i < len(values); i += 2 {
		if values[i] != values[i+1] {
			different++
		}
	}
	// Independent TPDF sequences agree on a null sample with probability
	// 1/8² + 3/4² + 1/8² = 0.59375, so a large share must differ.
	assert.Greater(t, different, frames/5, "the channels must not share the same noise")
}

// transientWriterSeeker fails exactly one write and accepts everything after
// it, which exercises the header repair path.
type transientWriterSeeker struct {
	inner  writerseeker.WriterSeeker
	failAt int // number of successful writes before the failure
	writes int
	err    error
	failed bool
}

func (w *transientWriterSeeker) Write(p []byte) (int, error) {
	if !w.failed && w.writes >= w.failAt {
		w.failed = true
		return 0, w.err
	}
	w.writes++
	return w.inner.Write(p)
}

func (w *transientWriterSeeker) Seek(offset int64, whence int) (int64, error) {
	return w.inner.Seek(offset, whence)
}

func TestEncodeErrorRepairsHeaderAfterTransientFailure(t *testing.T) {
	format := beep.Format{SampleRate: 44100, NumChannels: 2, Precision: 2}
	samples := sineSamples(t, 5000, 300)
	writeErr := errors.New("temporary write failure")

	w := &transientWriterSeeker{failAt: 3, err: writeErr}
	err := EncodeWithOptions(w, newChunkedStreamer(samples, 128), format, EncodeOptions{Dither: true, Seed: 2})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "temporary write failure")

	written := writerBytes(t, &w.inner)
	require.GreaterOrEqual(t, len(written), 44)
	fileSize, dataSize, payload := wavData(t, written)
	assert.Equal(t, dataSize, int32(len(payload)), "the repaired size must describe exactly the payload present")
	assert.Equal(t, int32(44-8)+dataSize, fileSize)
	assert.Less(t, dataSize, int32(len(samples)*format.Width()), "the file must be shorter than a complete one")

	// The repaired file decodes cleanly and yields exactly the declared frames.
	s, decodedFormat, err := Decode(bytes.NewReader(written))
	require.NoError(t, err)
	assert.Equal(t, format, decodedFormat)
	assert.Len(t, testtools.Collect(s), int(dataSize)/format.Width())
}
