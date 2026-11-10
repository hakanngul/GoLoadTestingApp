//go:build unix

package runner

import (
	"runtime"
	"syscall"
	"time"
)

// cpuMeter reports this process's CPU usage as a fraction of all cores.
type cpuMeter struct {
	lastWall time.Time
	lastCPU  time.Duration
}

func newCPUMeter() *cpuMeter {
	return &cpuMeter{lastWall: time.Now(), lastCPU: processCPU()}
}

func (m *cpuMeter) sample() float64 {
	now, cpu := time.Now(), processCPU()
	wall := now.Sub(m.lastWall)
	used := cpu - m.lastCPU
	m.lastWall, m.lastCPU = now, cpu
	if wall <= 0 {
		return 0
	}
	return float64(used) / float64(wall) / float64(runtime.NumCPU())
}

func processCPU() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
