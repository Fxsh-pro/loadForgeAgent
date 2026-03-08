package metrics

import (
	"math"
	"sort"
	"sync"
	"time"
)

type Sample struct {
	LatencyMs  float64
	Success    bool
	RecordedAt time.Time
}

type Collector struct {
	mu      sync.Mutex
	samples []Sample
}

func NewCollector() *Collector {
	return &Collector{}
}

func (c *Collector) Record(latencyMs float64, success bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samples = append(c.samples, Sample{
		LatencyMs:  latencyMs,
		Success:    success,
		RecordedAt: time.Now(),
	})
}

type Snapshot struct {
	RPS        float64
	LatencyP50 float64
	LatencyP90 float64
	LatencyP99 float64
	ErrorRate  float64
	Time       time.Time
}

// Flush atomically drains accumulated samples and computes metrics for the interval.
func (c *Collector) Flush(intervalSeconds float64) Snapshot {
	c.mu.Lock()
	samples := c.samples
	c.samples = nil
	c.mu.Unlock()

	snap := Snapshot{Time: time.Now().UTC()}

	if len(samples) == 0 {
		return snap
	}

	latencies := make([]float64, 0, len(samples))
	errors := 0

	for _, s := range samples {
		latencies = append(latencies, s.LatencyMs)
		if !s.Success {
			errors++
		}
	}

	sort.Float64s(latencies)

	snap.RPS = float64(len(samples)) / intervalSeconds
	snap.LatencyP50 = percentile(latencies, 50)
	snap.LatencyP90 = percentile(latencies, 90)
	snap.LatencyP99 = percentile(latencies, 99)
	snap.ErrorRate = float64(errors) / float64(len(samples))

	return snap
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := p / 100.0 * float64(len(sorted)-1)
	lower := int(math.Floor(idx))
	upper := int(math.Ceil(idx))
	if lower == upper {
		return sorted[lower]
	}
	frac := idx - float64(lower)
	return sorted[lower]*(1-frac) + sorted[upper]*frac
}
