package glance

import (
	"math"
	"testing"
	"time"
)

func TestCPUPercentBetween(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		prev, cur   cpuSample
		wantPercent float64
		wantOK      bool
	}{
		{
			// 1.5s of CPU consumed across a 10s window is 15% of one core.
			name:        "quarter of one core",
			prev:        cpuSample{cpuSeconds: 10, at: base},
			cur:         cpuSample{cpuSeconds: 11.5, at: base.Add(10 * time.Second)},
			wantPercent: 15,
			wantOK:      true,
		},
		{
			// A multithreaded process can consume more CPU time than wall time.
			name:        "three cores saturated exceeds 100%",
			prev:        cpuSample{cpuSeconds: 0, at: base},
			cur:         cpuSample{cpuSeconds: 30, at: base.Add(10 * time.Second)},
			wantPercent: 300,
			wantOK:      true,
		},
		{
			name:        "idle process reports zero",
			prev:        cpuSample{cpuSeconds: 42, at: base},
			cur:         cpuSample{cpuSeconds: 42, at: base.Add(15 * time.Second)},
			wantPercent: 0,
			wantOK:      true,
		},
		{
			// The priming sample in initialize() is only milliseconds old when
			// the first update runs, which would produce a meaningless figure.
			name:   "window shorter than the minimum is rejected",
			prev:   cpuSample{cpuSeconds: 10, at: base},
			cur:    cpuSample{cpuSeconds: 10.1, at: base.Add(100 * time.Millisecond)},
			wantOK: false,
		},
		{
			name:   "unprimed previous sample is rejected",
			prev:   cpuSample{},
			cur:    cpuSample{cpuSeconds: 10, at: base},
			wantOK: false,
		},
		{
			// CPU counters only move forwards, so this means the samples are
			// not comparable rather than that usage was negative.
			name:   "counter going backwards is rejected",
			prev:   cpuSample{cpuSeconds: 50, at: base},
			cur:    cpuSample{cpuSeconds: 10, at: base.Add(10 * time.Second)},
			wantOK: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			percent, ok := cpuPercentBetween(test.prev, test.cur)

			if ok != test.wantOK {
				t.Fatalf("ok = %v, want %v", ok, test.wantOK)
			}

			if ok && math.Abs(percent-test.wantPercent) > 0.001 {
				t.Errorf("percent = %v, want %v", percent, test.wantPercent)
			}
		})
	}
}

func TestPercentToBar(t *testing.T) {
	tests := []struct {
		percent float64
		want    uint8
	}{
		{percent: 0, want: 0},
		{percent: 42.7, want: 42},
		{percent: 100, want: 100},
		// A process using more than one core would otherwise overflow the uint8
		// and wrap around to a small number.
		{percent: 375, want: 100},
		{percent: -5, want: 0},
	}

	for _, test := range tests {
		if got := percentToBar(test.percent); got != test.want {
			t.Errorf("percentToBar(%v) = %d, want %d", test.percent, got, test.want)
		}
	}
}
