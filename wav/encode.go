package wav

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/pkg/errors"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/internal/util"
)

// EncodeOptions configures optional features of the WAV encoder.
//
// By default the bit depth is selected by the Precision field of the
// beep.Format passed to the encoder: precisions 1, 2 and 3 correspond to 8, 16
// and 24-bit PCM. BitDepth overrides it when set.
type EncodeOptions struct {
	// BitDepth selects the bit depth of the output when non-zero. Supported
	// values are 8, 16 and 24; any other value makes the encoder return an
	// error. When zero, the precision of the beep.Format is used.
	BitDepth int

	// Precision selects the bit depth in bytes when non-zero, mirroring
	// beep.Format.Precision. It takes precedence over BitDepth. Supported
	// values are 1, 2 and 3 (8, 16 and 24-bit PCM). It is an error to set both
	// Precision and BitDepth to values that disagree.
	Precision int

	// Dither enables TPDF (triangular probability density function) dithering
	// before quantization. For every output channel of every frame, two
	// independent uniformly distributed values each spanning one LSB peak to
	// peak are generated and their difference is added to the sample before
	// rounding to the nearest quantization level. The resulting noise has a
	// triangular distribution spanning two LSB peak to peak and zero mean.
	//
	// Dithering is supported for every precision accepted by the encoder, but
	// it is most useful when reducing the bit depth, for example to 16 bits.
	//
	// Samples at full scale saturate to the largest and smallest representable
	// values; the dither noise never makes them wrap around.
	Dither bool

	// Seed selects the deterministic pseudo-random sequence used for
	// dithering. The same input and seed produce byte-identical output
	// regardless of how the Streamer splits the stream into chunks. The seed
	// has no effect when Dither is false.
	Seed int64
}

// Encode writes all audio streamed from s to w in WAVE format.
//
// Format precision must be 1, 2 or 3 bytes (8, 16 or 24-bit PCM). Samples are
// quantized without dithering. Pass an EncodeOptions value with Dither set to
// enable TPDF dither, or use EncodeWithOptions or EncodeWithDither.
func Encode(w io.WriteSeeker, s beep.Streamer, format beep.Format, options ...EncodeOptions) error {
	if len(options) > 1 {
		return errors.Wrap(errors.New("at most one EncodeOptions value may be provided"), "wav")
	}
	var opts EncodeOptions
	if len(options) == 1 {
		opts = options[0]
	}
	return encode(w, s, format, opts)
}

// EncodeWithOptions writes all audio streamed from s to w in WAVE format,
// optionally applying TPDF dither before quantization.
//
// With options.Dither false, the output is byte-for-byte identical to Encode.
// With dithering enabled, every channel of every frame receives independent
// TPDF noise before round-to-nearest quantization. Full-scale samples saturate
// to the largest and smallest representable values instead of wrapping around.
//
// Format precision must be 1, 2 or 3 bytes (8, 16 or 24-bit PCM).
func EncodeWithOptions(w io.WriteSeeker, s beep.Streamer, format beep.Format, options EncodeOptions) error {
	return encode(w, s, format, options)
}

// EncodeWithDither is a convenience wrapper around EncodeWithOptions which
// enables dithering and uses the given seed.
//
// The same input and seed produce byte-identical output regardless of the
// chunk sizes returned by the Streamer.
func EncodeWithDither(w io.WriteSeeker, s beep.Streamer, format beep.Format, seed int64) error {
	return encode(w, s, format, EncodeOptions{Dither: true, Seed: seed})
}

func encode(w io.WriteSeeker, s beep.Streamer, format beep.Format, options EncodeOptions) (err error) {
	var (
		headerWritten bool
		bw            *bufio.Writer
	)
	defer func() {
		if err == nil {
			return
		}

		// The error may have happened after a valid (but not yet finalized)
		// header and some audio data were already written. Best-effort flush
		// and repair the length fields, so a partially written file keeps a
		// consistent header instead of the empty placeholder sizes.
		if headerWritten {
			if bw != nil {
				_ = bw.Flush()
			}
			_ = repairHeader(w, format.Width())
		}
		err = errors.Wrap(err, "wav")
	}()

	if format.NumChannels <= 0 {
		return errors.New("invalid number of channels (less than 1)")
	}

	// Resolve the bit depth. Options, when set, take precedence over the
	// precision of the format, so callers can select the output depth without
	// also changing the format their Streamer was prepared with.
	precision := format.Precision
	if options.Precision != 0 {
		precision = options.Precision
	}
	if options.BitDepth != 0 {
		switch options.BitDepth {
		case 8, 16, 24:
			precision = options.BitDepth / 8
		default:
			return fmt.Errorf("unsupported bit depth %d, 8, 16 or 24 is supported", options.BitDepth)
		}
	}
	if options.Precision != 0 && options.BitDepth != 0 && options.Precision != options.BitDepth/8 {
		return fmt.Errorf("conflicting precision %d bytes and bit depth %d", options.Precision, options.BitDepth)
	}
	if precision != 1 && precision != 2 && precision != 3 {
		return errors.New("unsupported precision, 1, 2 or 3 is supported")
	}
	format.Precision = precision

	var dither *tpdfDither
	if options.Dither {
		dither = newTPDFDither(options.Seed)
	}

	// The placeholder sizes deliberately describe an empty but valid file. If
	// streaming fails and the writer cannot be repaired afterwards, the file
	// never claims more audio than it contains.
	h := header{
		RiffMark:      [4]byte{'R', 'I', 'F', 'F'},
		FileSize:      44 - 8, // finalization
		WaveMark:      [4]byte{'W', 'A', 'V', 'E'},
		FmtMark:       [4]byte{'f', 'm', 't', ' '},
		FormatSize:    16,
		FormatType:    1,
		NumChans:      int16(format.NumChannels),
		SampleRate:    int32(format.SampleRate),
		ByteRate:      int32(int(format.SampleRate) * format.NumChannels * format.Precision),
		BytesPerFrame: int16(format.NumChannels * format.Precision),
		BitsPerSample: int16(format.Precision) * 8,
		DataMark:      [4]byte{'d', 'a', 't', 'a'},
		DataSize:      0, // finalization
	}
	if err := binary.Write(w, binary.LittleEndian, &h); err != nil {
		return err
	}
	headerWritten = true

	bw = bufio.NewWriter(w)

	var (
		samples = make([][2]float64, 512)
		buffer  = make([]byte, len(samples)*format.Width())
		written int
		frame   int
	)
	for {
		n, ok := s.Stream(samples)
		if n < 0 || n > len(samples) {
			return fmt.Errorf("streamer returned invalid sample count %d", n)
		}
		if n > 0 {
			if err := validateSamples(samples[:n], frame); err != nil {
				return err
			}

			buf := buffer
			for _, sample := range samples[:n] {
				buf = buf[encodeFrame(format, buf, sample, dither):]
			}

			nn, err := bw.Write(buffer[:n*format.Width()])
			if err != nil {
				return err
			}
			written += nn
			frame += n
		}
		if !ok {
			break
		}
		if n == 0 {
			// The Streamer contract allows only (0, false) once drained. Any
			// other empty result would make the encoder loop forever.
			return errors.New("streamer returned no samples without being drained")
		}
	}
	if err := s.Err(); err != nil {
		return errors.Wrap(err, "streamer")
	}
	if err := bw.Flush(); err != nil {
		return err
	}

	// finalize header
	if int64(written) > math.MaxInt32-(44-8) {
		return errors.New("audio data too large for WAVE format")
	}
	h.FileSize = int32(44 - 8 + written) // 44-8 is the size of the header without RIFF signature and the length of the RIFF chunk
	h.DataSize = int32(written)
	if _, err := w.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, &h); err != nil {
		return err
	}
	if _, err := w.Seek(0, io.SeekEnd); err != nil {
		return err
	}

	return nil
}

// validateSamples rejects NaN and infinite values. They cannot be quantized
// meaningfully, and silently converting them would produce arbitrary samples.
func validateSamples(samples [][2]float64, frame int) error {
	for i, sample := range samples {
		for c, x := range sample {
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return fmt.Errorf("non-finite sample at frame %d, channel %d", frame+i, c)
			}
		}
	}
	return nil
}

// encodeFrame encodes a single frame. Without dithering it delegates to
// beep.Format's encoder, preserving the exact bytes produced by Encode.
func encodeFrame(format beep.Format, p []byte, sample [2]float64, dither *tpdfDither) int {
	if dither == nil {
		if format.Precision == 1 {
			return format.EncodeUnsigned(p, sample)
		}
		return format.EncodeSigned(p, sample)
	}

	// The dither sequence advances once per output channel and frame,
	// independently of how the Streamer chunks its output. This is what makes
	// the output identical for every chunk size.
	if format.NumChannels == 1 {
		x := util.Clamp((sample[0]+sample[1])/2, -1.0, 1.0)
		encodeDithered(p[:format.Precision], x, format.Precision, dither)
		return format.Width()
	}

	for c := 0; c < format.NumChannels; c++ {
		var x float64
		if c < len(sample) {
			x = util.Clamp(sample[c], -1.0, 1.0)
		}
		start := c * format.Precision
		encodeDithered(p[start:start+format.Precision], x, format.Precision, dither)
	}
	return format.Width()
}

// encodeDithered adds TPDF noise to x and quantizes it with round-to-nearest.
// x must already be clamped to [-1, 1].
func encodeDithered(p []byte, x float64, precision int, dither *tpdfDither) {
	// Always advance the sequence, even for full-scale samples. This keeps the
	// noise of a given frame and channel determined solely by its position,
	// never by the sample values or the Streamer's chunking.
	noise := dither.next()

	if precision == 1 {
		// Unsigned 8-bit PCM maps [-1, 1] to [0, 255], so one LSB spans
		// 2/256 = 1/128 of the full-scale range.
		switch {
		case x >= 1:
			p[0] = 0xff
		case x <= -1:
			p[0] = 0x00
		default:
			v := math.Round((x+1)*128 + noise)
			p[0] = byte(util.Clamp(v, 0, 255))
		}
		return
	}

	// Signed PCM: one LSB spans 1/2^(bits-1) of the full-scale range. The most
	// negative value is representable exactly, while the most positive one is
	// one step smaller. Clamping here saturates full-scale samples instead of
	// wrapping them around.
	scale := math.Exp2(float64(precision*8 - 1))
	var v float64
	switch {
	case x >= 1:
		v = scale - 1
	case x <= -1:
		v = -scale
	default:
		v = util.Clamp(math.Round(x*scale+noise), -scale, scale-1)
	}

	u := uint64(int64(v))
	for i := 0; i < precision; i++ {
		p[i] = byte(u)
		u >>= 8
	}
}

// tpdfDither produces triangular PDF noise from the difference of two
// independent uniform values in [0, 1). The noise has zero mean and spans two
// LSB peak to peak.
//
// splitmix64 is used directly so the sequence is fully specified by the seed
// and stable across Go versions and platforms.
type tpdfDither struct {
	state uint64
}

func newTPDFDither(seed int64) *tpdfDither {
	return &tpdfDither{state: uint64(seed)}
}

func (d *tpdfDither) next() float64 {
	return d.uniform() - d.uniform()
}

func (d *tpdfDither) uniform() float64 {
	d.state += 0x9e3779b97f4a7c15
	z := d.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	z ^= z >> 31
	// Use the top 53 bits so every value is exactly representable and the
	// result lies in [0, 1).
	return float64(z>>11) * (1.0 / (1 << 53))
}

// repairHeader rewrites the RIFF and data chunk sizes using the actual length
// of w. The data size is rounded down to a whole number of frames. It is used
// on the error path, so a partially encoded file reports the audio it actually
// contains instead of the empty placeholder sizes.
func repairHeader(w io.WriteSeeker, frameWidth int) error {
	end, err := w.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	const headerSize = 44
	if end < headerSize || frameWidth <= 0 {
		return nil
	}
	dataSize := (end - headerSize) / int64(frameWidth) * int64(frameWidth)
	if dataSize > math.MaxInt32-(headerSize-8) {
		return nil
	}
	if _, err := w.Seek(4, io.SeekStart); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, int32(headerSize-8+dataSize)); err != nil {
		return err
	}
	if _, err := w.Seek(40, io.SeekStart); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, int32(dataSize)); err != nil {
		return err
	}
	_, err = w.Seek(0, io.SeekEnd)
	return err
}
