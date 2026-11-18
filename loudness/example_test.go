package loudness_test

import (
	"fmt"
	"math"
	"time"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/generators"
	"github.com/gopxl/beep/v2/loudness"
)

// Wrap a streamer with a Meter and poll the three LUFS measurements while the
// audio plays through unchanged.
func ExampleMeter() {
	sampleRate := beep.SampleRate(48000)

	sine, err := generators.SineTone(sampleRate, 440)
	if err != nil {
		panic(err)
	}
	// Five seconds of full-scale stereo tone, then the streamer drains.
	tone := beep.Take(sampleRate.N(5*time.Second), sine)

	meter, err := loudness.NewMeter(sampleRate, tone)
	if err != nil {
		panic(err)
	}

	buf := make([][2]float64, sampleRate.N(time.Second/10))
	for {
		_, ok := meter.Stream(buf)
		if !ok {
			break
		}
	}

	// A full-scale stereo sine measures close to 0 LUFS (the K-weighting
	// curve slightly attenuates 440 Hz, hence -0.73); SilenceLUFS (-Inf)
	// would be reported instead for an all-silent stream.
	fmt.Printf("integrated loudness: %.2f LUFS\n", meter.Integrated())
	fmt.Printf("all-silent reads as -Inf: %v\n", math.IsInf(loudness.SilenceLUFS, -1))
	// Output:
	// integrated loudness: -0.73 LUFS
	// all-silent reads as -Inf: true
}
