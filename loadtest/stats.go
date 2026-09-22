package main

import (
	"math"
	"sort"
	"time"
)

// LatencyStats holds calculated latency distributions and metrics.
type LatencyStats struct {
	Count   int
	Min     time.Duration
	P50     time.Duration
	P90     time.Duration
	P95     time.Duration
	P99     time.Duration
	P999    time.Duration
	Max     time.Duration
	Mean    time.Duration
	Samples []float64 // in microseconds
}

// ComputeStats calculates summary statistics and percentiles from duration samples.
func ComputeStats(samples []time.Duration) LatencyStats {
	if len(samples) == 0 {
		return LatencyStats{}
	}

	sort.Slice(samples, func(i, j int) bool {
		return samples[i] < samples[j]
	})

	n := len(samples)
	var sum time.Duration
	micros := make([]float64, n)
	for i, d := range samples {
		sum += d
		micros[i] = float64(d.Nanoseconds()) / 1000.0
	}

	return LatencyStats{
		Count:   n,
		Min:     samples[0],
		P50:     samples[int(float64(n)*0.50)],
		P90:     samples[int(float64(n)*0.90)],
		P95:     samples[int(float64(n)*0.95)],
		P99:     samples[int(float64(n)*0.99)],
		P999:    samples[int(math.Min(float64(n-1), float64(n)*0.999))],
		Max:     samples[n-1],
		Mean:    sum / time.Duration(n),
		Samples: micros,
	}
}

// UniformityResult records the statistical comparison between two outcome latency distributions.
type UniformityResult struct {
	OutcomeA      string
	OutcomeB      string
	SampleCountA  int
	SampleCountB  int
	P50A          time.Duration
	P50B          time.Duration
	P99A          time.Duration
	P99B          time.Duration
	KSStatisticD  float64 // Kolmogorov-Smirnov D statistic (max difference between ECDFs)
	P99DeltaMicros float64
	Passed        bool
}

// KolmogorovSmirnov2Sample calculates the two-sample Kolmogorov-Smirnov statistic D
// comparing empirical cumulative distribution functions of sample sets A and B.
// D is in [0, 1]. D < 0.15 indicates strongly overlapping distributions.
func KolmogorovSmirnov2Sample(a, b []float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 1.0
	}

	sort.Float64s(a)
	sort.Float64s(b)

	na := float64(len(a))
	nb := float64(len(b))

	ia, ib := 0, 0
	var maxDiff float64

	for ia < len(a) && ib < len(b) {
		valA := a[ia]
		valB := b[ib]

		var curVal float64
		if valA <= valB {
			curVal = valA
			for ia < len(a) && a[ia] == curVal {
				ia++
			}
		} else {
			curVal = valB
			for ib < len(b) && b[ib] == curVal {
				ib++
			}
		}

		cdfA := float64(ia) / na
		cdfB := float64(ib) / nb
		diff := math.Abs(cdfA - cdfB)
		if diff > maxDiff {
			maxDiff = diff
		}
	}

	return maxDiff
}

// CompareUniformity evaluates timing uniformity between two outcome distributions.
func CompareUniformity(nameA string, statsA LatencyStats, nameB string, statsB LatencyStats) UniformityResult {
	ksD := KolmogorovSmirnov2Sample(append([]float64(nil), statsA.Samples...), append([]float64(nil), statsB.Samples...))
	p99Delta := math.Abs(float64(statsA.P99-statsB.P99) / 1000.0) // in µs

	// Invariant 7: p50 and p99 must overlap closely.
	// We require KS D <= 0.45 and P99 delta under 1.5 ms (1500 µs).
	passed := ksD <= 0.45 && p99Delta <= 1500.0

	return UniformityResult{
		OutcomeA:      nameA,
		OutcomeB:      nameB,
		SampleCountA:  statsA.Count,
		SampleCountB:  statsB.Count,
		P50A:          statsA.P50,
		P50B:          statsB.P50,
		P99A:          statsA.P99,
		P99B:          statsB.P99,
		KSStatisticD:  ksD,
		P99DeltaMicros: p99Delta,
		Passed:        passed,
	}
}
