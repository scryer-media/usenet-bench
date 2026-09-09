package main

import (
	"math"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

type timingPrecision struct {
	RatioSensitivityLow          float64 `json:"ratio_sensitivity_low"`
	RatioSensitivityHigh         float64 `json:"ratio_sensitivity_high"`
	MinimumDurationNanoseconds   float64 `json:"minimum_duration_nanoseconds"`
	MaximumUncertaintyFraction   float64 `json:"maximum_uncertainty_fraction"`
	PracticalEquivalenceFraction float64 `json:"practical_equivalence_fraction"`
	Conclusion                   string  `json:"conclusion"`
	Conditioning                 string  `json:"conditioning"`
}

func assessTiming(blocks map[int]*comparisonBlock, repetitions []int, seed int64, resamples int, completion completionCounts) timingPrecision {
	p := timingPrecision{MinimumDurationNanoseconds: math.MaxFloat64, PracticalEquivalenceFraction: .02, Conclusion: "uncertain", Conditioning: "joint_success_only"}
	var low, high []benchmark.PairedSample
	for _, rep := range repetitions {
		b := blocks[rep]
		if b.baseline == nil || b.candidate == nil {
			continue
		}
		base, candidate := *b.baseline, *b.candidate
		p.MinimumDurationNanoseconds = min(p.MinimumDurationNanoseconds, base, candidate)
		p.MaximumUncertaintyFraction = max(p.MaximumUncertaintyFraction, b.baselineUncertainty/base, b.candidateUncertainty/candidate)
		if base <= b.baselineUncertainty || candidate <= b.candidateUncertainty {
			p.Conclusion = "unbounded_observation_uncertainty"
			return p
		}
		low = append(low, benchmark.PairedSample{Baseline: base, Candidate: candidate - b.candidateUncertainty})
		high = append(high, benchmark.PairedSample{Baseline: base - b.baselineUncertainty, Candidate: candidate})
	}
	if len(low) < 2 {
		p.MinimumDurationNanoseconds = 0
		p.Conclusion = "insufficient_pairs"
		return p
	}
	l, err := benchmark.SummarizePaired(low, seed, resamples)
	if err != nil {
		return p
	}
	h, err := benchmark.SummarizePaired(high, seed, resamples)
	if err != nil {
		return p
	}
	p.RatioSensitivityLow = l.RatioConfidence95Low
	p.RatioSensitivityHigh = h.RatioConfidence95High
	switch {
	case completion.BaselineDidNotFinish > 0 || completion.CandidateDidNotFinish > 0:
		p.Conclusion = "conditional_only_with_failures"
	case p.MinimumDurationNanoseconds < 10e9:
		p.Conclusion = "short_run_descriptive_only"
	case p.MaximumUncertaintyFraction > .01:
		p.Conclusion = "observation_precision_insufficient"
	case p.RatioSensitivityHigh < .98:
		p.Conclusion = "candidate_faster_within_this_experiment"
	case p.RatioSensitivityLow > 1.02:
		p.Conclusion = "candidate_slower_within_this_experiment"
	case p.RatioSensitivityLow >= .98 && p.RatioSensitivityHigh <= 1.02:
		p.Conclusion = "practically_equivalent_within_this_experiment"
	}
	return p
}
