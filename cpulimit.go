package cpulimit

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/process"
)

const (
	// DefaultLimit is 80%
	DefaultLimit float64 = 80.0
	// DefaultInterval is 333 ms
	DefaultInterval time.Duration = time.Millisecond * 333
	// DefaultMeasurements is 3
	DefaultMeasurements int = 3
)

// Limiter limits the CPU usage
type Limiter struct {
	// MaxCPUUsage specifies the maximum CPU usage; wait will block if the
	// average CPU usage during the previous measurements exceeds this value.
	MaxCPUUsage float64
	// MeasureInterval specifies how often the CPU usage should be measured.
	MeasureInterval time.Duration
	// Measurements specifies how many measurements should be retained for the
	// average CPU usage calculation.
	Measurements int
	// CurrentProcessOnly specifies that only the CPU usage of the current process
	// should be measured; otherwise, the full CPU usage is measured.
	CurrentProcessOnly bool

	stop bool
	wg   *sync.WaitGroup
	self *process.Process

	// usageBelowLimit will be blocking while the CPU usage is above MaxCPUUsage,
	// and will be closed when the CPU usage is below MaxCPUUsage.
	usageBelowLimit chan struct{}
}

// Start starts the CPU limiter. If there are undefined variables
// Start() will set them to the default values.
func (l *Limiter) Start() error {
	if l.MaxCPUUsage == 0.0 {
		l.MaxCPUUsage = DefaultLimit
	}
	if l.MeasureInterval == 0 {
		l.MeasureInterval = DefaultInterval
	}
	if l.Measurements == 0 {
		l.Measurements = DefaultMeasurements
	}
	l.wg = &sync.WaitGroup{}
	l.wg.Add(1)
	l.usageBelowLimit = make(chan struct{})
	if l.CurrentProcessOnly {
		var err error
		l.self, err = process.NewProcess(int32(os.Getpid()))
		if err != nil {
			return err
		}
	}
	go l.run()
	return nil
}

// Stop stops the limiter. After this stop, Wait() won't block anymore.
func (l *Limiter) Stop() {
	l.stop = true
	l.wg.Wait()
}

// Wait waits until the CPU usage is below MaxCPUUsage
func (l *Limiter) Wait() {
	l.WaitContext(context.Background())
}

func (l *Limiter) WaitContext(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	select {
	case <-l.usageBelowLimit:
	case <-ctx.Done():
	}
}

// AboveLimit reports if the CPU usage is above MaxCPUUsage
func (l *Limiter) AboveLimit() bool {
	select {
	case <-l.usageBelowLimit:
		return false
	default:
		return true
	}
}

func (l *Limiter) run() {
	defer l.wg.Done()
	var (
		busy1    float64
		busy2    float64
		all1     float64
		all2     float64
		cpuUsage float64
		locked   = true
		m        = make([]float64, l.Measurements) // measurements
	)
	tk := time.NewTicker(l.MeasureInterval)
	defer tk.Stop()
	busy2, all2 = l.getBusy()
	var counter int
	for range tk.C {
		if l.stop {
			if locked {
				close(l.usageBelowLimit)
			}
			break
		}
		busy1, all1 = busy2, all2
		busy2, all2 = l.getBusy()
		cpuUsage = getCPUUsage(busy1, all1, busy2, all2)
		m[counter] = cpuUsage
		if average(m) > l.MaxCPUUsage {
			if !locked {
				// Create a new, blocking channel to signal that the CPU usage is above the limit
				l.usageBelowLimit = make(chan struct{})
				locked = true
			}
		} else {
			if locked {
				// Close the channel to signal that the CPU usage is below the limit
				close(l.usageBelowLimit)
				locked = false
			}
		}
		counter++
		if counter > l.Measurements-1 {
			counter = 0
		}
	}
}

func average(m []float64) (avg float64) {
	for _, elem := range m {
		avg += elem
	}
	avg /= float64(len(m))
	return
}

func (l *Limiter) getBusy() (busy, all float64) {
	if l.CurrentProcessOnly {
		busy, _ = busyFromTimes(getProcessCpuTimes(l.self))
		_, all = busyFromTimes(getGlobalCpuTimes())
	} else {
		busy, all = busyFromTimes(getGlobalCpuTimes())
	}
	return
}

func getGlobalCpuTimes() cpu.TimesStat {
	ts, err := cpu.Times(false)
	if err != nil {
		panic(err)
	}
	return ts[0]
}

func getProcessCpuTimes(proc *process.Process) cpu.TimesStat {
	t, err := proc.Times()
	if err != nil {
		panic(err)
	}
	return *t
}

func busyFromTimes(t cpu.TimesStat) (busy, all float64) {
	busy = t.User + t.System + t.Nice + t.Iowait + t.Irq + t.Softirq + t.Steal + t.Guest + t.GuestNice
	all = busy + t.Idle
	return
}

func getCPUUsage(busy1, all1, busy2, all2 float64) float64 {
	if all1 == all2 {
		return 0.0
	}
	usage := ((busy2 - busy1) / (all2 - all1)) * 100.0
	return usage
}
