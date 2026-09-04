package glance

import (
	"context"
	"errors"
	"html/template"
	"os"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

var processStatsWidgetTemplate = mustParseTemplate("process-stats.html", "widget-base.html")

// Sampling windows shorter than this produce meaningless CPU percentages, as
// the process may not have been scheduled at all during the interval.
const minCPUSampleWindow = 2 * time.Second

type processStatsWidget struct {
	widgetBase `yaml:",inline"`

	Stats processStats `yaml:"-"`

	proc       *process.Process `yaml:"-"`
	lastSample cpuSample        `yaml:"-"`
}

// processStats is everything the template renders. Percentages are top-style:
// 100% means one core fully saturated, so a multithreaded process can exceed it.
type processStats struct {
	IsAvailable bool
	PID         int

	CPUPercent float64
	// CPUPercent scaled against total machine capacity, so a progress bar has a
	// meaningful 0-100 range. Mirrors how sysinfo normalises load by core count.
	CPUBarPercent uint8
	// True while we are still showing the lifetime average because no rolling
	// window is available yet.
	CPUIsLifetimeAverage bool

	MemoryMB    uint64
	GoHeapMB    uint64
	MemoryPct   uint8
	Goroutines  int
	StartedAt   time.Time
	StartedIsOK bool
}

// cpuSample is a point-in-time reading of the process's consumed CPU time.
type cpuSample struct {
	cpuSeconds float64
	at         time.Time
}

func (widget *processStatsWidget) initialize() error {
	widget.withTitle("Glance").withCacheDuration(15 * time.Second)

	proc, err := process.NewProcess(int32(os.Getpid()))
	if err != nil {
		return errors.New("looking up own process: " + err.Error())
	}
	widget.proc = proc

	// Prime the sampler so the first update has a window to measure against.
	// gopsutil's own Percent() keeps this state internally, which is why the
	// process handle must be created once here rather than on every update.
	if sample, err := widget.sample(context.Background()); err == nil {
		widget.lastSample = sample
	}

	return nil
}

func (widget *processStatsWidget) update(ctx context.Context) {
	stats, err := widget.collect(ctx)
	if !widget.canContinueUpdateAfterHandlingErr(err) {
		return
	}

	widget.Stats = stats
}

func (widget *processStatsWidget) Render() template.HTML {
	return widget.renderTemplate(widget, processStatsWidgetTemplate)
}

func (widget *processStatsWidget) sample(ctx context.Context) (cpuSample, error) {
	times, err := widget.proc.TimesWithContext(ctx)
	if err != nil {
		return cpuSample{}, err
	}

	return cpuSample{cpuSeconds: times.User + times.System, at: time.Now()}, nil
}

func (widget *processStatsWidget) collect(ctx context.Context) (processStats, error) {
	stats := processStats{PID: os.Getpid(), Goroutines: runtime.NumGoroutine()}

	sample, err := widget.sample(ctx)
	if err != nil {
		return stats, errors.New("reading process CPU times: " + err.Error())
	}

	if percent, ok := cpuPercentBetween(widget.lastSample, sample); ok {
		stats.CPUPercent = percent
	} else {
		// No usable window yet — on the very first update the priming sample is
		// only milliseconds old. Showing the lifetime average is honest; showing
		// a hard 0% would not be.
		lifetime, err := widget.proc.CPUPercentWithContext(ctx)
		if err != nil {
			return stats, errors.New("reading process CPU percent: " + err.Error())
		}
		stats.CPUPercent = lifetime
		stats.CPUIsLifetimeAverage = true
	}
	widget.lastSample = sample

	stats.CPUBarPercent = percentToBar(stats.CPUPercent / float64(runtime.NumCPU()))

	memory, err := widget.proc.MemoryInfoWithContext(ctx)
	if err != nil {
		return stats, errors.New("reading process memory: " + err.Error())
	}
	stats.MemoryMB = memory.RSS / 1024 / 1024

	memoryPct, err := widget.proc.MemoryPercentWithContext(ctx)
	if err == nil {
		stats.MemoryPct = percentToBar(float64(memoryPct))
	}

	// The Go heap is a subset of RSS: the runtime holds on to arenas it has
	// freed but not returned to the OS, so these two legitimately disagree.
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	stats.GoHeapMB = mem.HeapAlloc / 1024 / 1024

	if createdMs, err := widget.proc.CreateTimeWithContext(ctx); err == nil {
		stats.StartedAt = time.UnixMilli(createdMs)
		stats.StartedIsOK = true
	}

	stats.IsAvailable = true

	return stats, nil
}

// cpuPercentBetween returns the CPU used across the window between two samples,
// as a top-style percentage where 100% is one core fully saturated. ok is false
// when the window is too short or the samples are out of order, in which case
// the caller has nothing meaningful to display.
func cpuPercentBetween(prev, cur cpuSample) (float64, bool) {
	if prev.at.IsZero() {
		return 0, false
	}

	elapsed := cur.at.Sub(prev.at)
	if elapsed < minCPUSampleWindow {
		return 0, false
	}

	consumed := cur.cpuSeconds - prev.cpuSeconds
	if consumed < 0 {
		// Counters only ever move forwards; anything else means the samples
		// cannot be compared.
		return 0, false
	}

	return (consumed / elapsed.Seconds()) * 100, true
}

// percentToBar clamps a percentage into the 0-100 uint8 range the progress bar
// markup expects.
func percentToBar(percent float64) uint8 {
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}

	return uint8(percent)
}
