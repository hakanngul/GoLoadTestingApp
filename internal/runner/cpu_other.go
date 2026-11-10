//go:build !unix

package runner

// cpuMeter is a no-op where getrusage is unavailable.
type cpuMeter struct{}

func newCPUMeter() *cpuMeter        { return &cpuMeter{} }
func (m *cpuMeter) sample() float64 { return 0 }
