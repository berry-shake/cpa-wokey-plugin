package requestlog

// Every entry carries both output speeds the benchmarking tools report:
//
//   - Generation speed is the rate after the first token, (output − 1) ÷
//     (total − TTFT): vLLM's 1 / TPOT and NVIDIA GenAI-Perf's output token
//     throughput per user. Only a stream has a first token time; the first
//     token belongs to the TTFT, hence the − 1.
//   - End-to-end speed is output ÷ total time with the wait for the first
//     token included: Artificial Analysis's output speed and LLMPerf's
//     per-request output throughput. Buffered responses have it too.
//
// The speeds are worked out from the stored millisecond figures, so the
// same formulas over the stored columns give an old entry exactly what a new
// entry gets.

// minSpeedWindowMS is the shortest time a speed is worked out over; a few
// milliseconds make a meaningless rate.
const minSpeedWindowMS = 50

// GenerationSpeed is a stream's output tokens per second after the first
// token; nil without a first token time, with fewer than two output tokens
// or over less than minSpeedWindowMS.
func GenerationSpeed(stream bool, outputTokens, durationMS int64, ttftMS *int64) *float64 {
	if !stream || ttftMS == nil || outputTokens < 2 {
		return nil
	}
	window := durationMS - *ttftMS
	if window <= minSpeedWindowMS {
		return nil
	}
	rate := float64(outputTokens-1) * 1000 / float64(window)
	return &rate
}

// E2ESpeed is output tokens per second over the whole request; nil without
// output or over less than minSpeedWindowMS.
func E2ESpeed(outputTokens, durationMS int64) *float64 {
	if outputTokens <= 0 || durationMS <= minSpeedWindowMS {
		return nil
	}
	rate := float64(outputTokens) * 1000 / float64(durationMS)
	return &rate
}
