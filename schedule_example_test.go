package beep_test

import (
	"fmt"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/internal/testtools"
)

// ExampleMixer_Schedule shows how a Streamer can be scheduled to start at an absolute sample
// position on the Mixer's sample clock, and how the returned handle can cancel it.
func ExampleMixer_Schedule() {
	m := &beep.Mixer{}

	// A one-shot pulse occupying three samples, all with value 1.
	pulse := func() beep.Streamer {
		return testtools.NewDataStreamer([][2]float64{{1, 1}, {1, 1}, {1, 1}})
	}

	// Start the first pulse at sample 2 and the second at sample 5.
	m.Schedule(2, pulse())
	cancelled := m.Schedule(8, pulse())

	// Cancel the second pulse before its start position is reached.
	cancelled.Cancel()

	out := testtools.CollectNum(10, m)
	for i, s := range out {
		fmt.Printf("%d:%g ", i, s[0])
	}
	// Output:
	// 0:0 1:0 2:1 3:1 4:1 5:0 6:0 7:0 8:0 9:0
}
