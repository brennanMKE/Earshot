package earshot

import (
	"maps"
	"slices"
	"sync"
)

const sampleWindow = 1024

type sample struct {
	elapsedMS float64
	rtf       float64
}

type metrics struct {
	mu        sync.Mutex
	requests  uint64
	successes uint64
	errors    map[string]uint64
	samples   [sampleWindow]sample
	count     int
	next      int
}

func (m *metrics) record(problem *requestError, result Result) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.requests++
	if problem != nil {
		m.errors[problem.Code]++
		return
	}
	m.successes++
	if result.Duration <= 0 {
		return
	}
	elapsed := float64(result.ElapsedMS)
	m.samples[m.next] = sample{elapsedMS: elapsed, rtf: elapsed / 1000 / result.Duration}
	m.next = (m.next + 1) % sampleWindow
	if m.count < sampleWindow {
		m.count++
	}
}

type percentiles struct {
	P50 *float64 `json:"p50"`
	P95 *float64 `json:"p95"`
}

type stats struct {
	Requests       uint64            `json:"requests"`
	Successes      uint64            `json:"successes"`
	Errors         map[string]uint64 `json:"errors"`
	Samples        int               `json:"samples"`
	ElapsedMS      percentiles       `json:"elapsed_ms"`
	RealTimeFactor percentiles       `json:"real_time_factor"`
}

func (m *metrics) snapshot() stats {
	m.mu.Lock()
	result := stats{
		Requests: m.requests, Successes: m.successes,
		Errors: maps.Clone(m.errors), Samples: m.count,
	}
	samples := slices.Clone(m.samples[:m.count])
	m.mu.Unlock()

	elapsed := make([]float64, len(samples))
	rtf := make([]float64, len(samples))
	for i, value := range samples {
		elapsed[i] = value.elapsedMS
		rtf[i] = value.rtf
	}
	result.ElapsedMS = summarize(elapsed)
	result.RealTimeFactor = summarize(rtf)
	return result
}

func summarize(values []float64) percentiles {
	if len(values) == 0 {
		return percentiles{}
	}
	slices.Sort(values)
	return percentiles{
		P50: &values[(50*len(values)+99)/100-1],
		P95: &values[(95*len(values)+99)/100-1],
	}
}
