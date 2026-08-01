package optimizer

import (
	"math"
	"net/netip"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/benchmark"
	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/cf-optimizer/cf-optimizer/internal/store"
)

const historyWeight = 0.15

// Decision 描述一个地址族最终应保留的节点以及是否需要切换策略。
type Decision struct {
	Family       int              `json:"family"`
	HasSelection bool             `json:"has_selection"`
	Selected     benchmark.Result `json:"selected"`
	ShouldSwitch bool             `json:"should_switch"`
	Reason       string           `json:"reason"`
	Improvement  float64          `json:"improvement"`
}

// ApplyHistory 使用有限权重平滑当前分数，避免单次网络波动主导选择。
func ApplyHistory(results []benchmark.Result, nodes map[string]store.NodeStats) {
	for index := range results {
		stats, exists := nodes[results[index].IP.String()]
		if !exists || stats.Attempts == 0 || stats.AverageScore <= 0 {
			continue
		}
		currentWeight := 1 - historyWeight
		results[index].Score = roundScore(results[index].Score*currentWeight + stats.AverageScore*historyWeight)
	}
}

// RecordResults 更新节点成功率、历史均分和连续失败冷却状态。
func RecordResults(nodes map[string]store.NodeStats, results []benchmark.Result, cfg config.BenchmarkConfig, now time.Time) {
	for _, result := range results {
		key := result.IP.String()
		stats := nodes[key]
		stats.Attempts++
		stats.LastTestedAt = now.UTC()
		if result.Qualified {
			previousSuccesses := stats.Successes
			stats.Successes++
			stats.FailureStreak = 0
			stats.CooldownUntil = time.Time{}
			stats.AverageScore = roundScore((stats.AverageScore*float64(previousSuccesses) + result.Score) / float64(stats.Successes))
		} else {
			stats.FailureStreak++
			if stats.FailureStreak >= cfg.FailureThreshold {
				stats.CooldownUntil = now.Add(cfg.FailureCooldown.Duration()).UTC()
			}
		}
		nodes[key] = stats
	}
}

// Decide 按当前健康度、最短保持时间和改善阈值选择一个地址族的节点。
func Decide(results []benchmark.Result, current *store.Selection, family int, cfg config.BenchmarkConfig, now time.Time) Decision {
	decision := Decision{Family: family, Reason: "no-qualified-candidate"}
	best, hasBest := bestForFamily(results, family)
	if !hasBest {
		return decision
	}
	if current == nil || current.IP == "" {
		decision.HasSelection = true
		decision.Selected = best
		decision.ShouldSwitch = true
		decision.Reason = "initial-selection"
		return decision
	}

	currentResult, currentWasTested := resultForIP(results, current.IP)
	if best.IP.String() == current.IP {
		decision.HasSelection = true
		decision.Selected = best
		decision.Reason = "current-remains-best"
		return decision
	}
	if current.ConsecutiveFailures >= cfg.FailureThreshold {
		return switchTo(best, family, "current-failure-threshold", current.Score)
	}
	if !currentWasTested || !currentResult.Qualified {
		return switchTo(best, family, "current-not-qualified", current.Score)
	}

	decision.HasSelection = true
	decision.Selected = currentResult
	baseline := currentResult.Score
	if baseline <= 0 {
		baseline = current.Score
	}
	decision.Improvement = relativeImprovement(best.Score, baseline)
	if now.Sub(current.SelectedAt) < cfg.MinimumHold.Duration() {
		decision.Reason = "minimum-hold-active"
		return decision
	}
	if decision.Improvement < cfg.SwitchImprovement {
		decision.Reason = "improvement-below-threshold"
		return decision
	}
	return switchTo(best, family, "better-candidate", baseline)
}

func bestForFamily(results []benchmark.Result, family int) (benchmark.Result, bool) {
	for _, result := range results {
		if result.Family == family && result.Qualified && result.Score > 0 {
			return result, true
		}
	}
	return benchmark.Result{}, false
}

func resultForIP(results []benchmark.Result, rawIP string) (benchmark.Result, bool) {
	ip, err := netip.ParseAddr(rawIP)
	if err != nil {
		return benchmark.Result{}, false
	}
	for _, result := range results {
		if result.IP == ip {
			return result, true
		}
	}
	return benchmark.Result{}, false
}

func switchTo(result benchmark.Result, family int, reason string, baseline float64) Decision {
	return Decision{
		Family: family, HasSelection: true, Selected: result, ShouldSwitch: true,
		Reason: reason, Improvement: relativeImprovement(result.Score, baseline),
	}
}

func relativeImprovement(candidate, baseline float64) float64 {
	if baseline <= 0 {
		return math.Inf(1)
	}
	return (candidate - baseline) / baseline
}

func roundScore(value float64) float64 {
	return math.Round(value*100) / 100
}
