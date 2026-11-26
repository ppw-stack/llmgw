package router

import (
	"hash/fnv"
	"sort"
	"sync/atomic"
	"time"

	"github.com/pw/llmgw/internal/config"
)

// candidateState is the routing state of one candidate at a given moment.
type candidateState struct {
	// Priority is the priority the candidate takes right now.
	Priority int
	// Active reports whether the candidate may serve the model at all.
	Active bool
	// Window is the window that decides the priority, if any.
	Window *config.EffectiveWindow
	// Hit reports whether a window with a priority is active.
	Hit bool
	// QuotaKnown reports whether cached subscription quota data exists.
	QuotaKnown bool
	// QuotaUsedPct is the peak window usage percentage (0-100) when known.
	QuotaUsedPct float64
	// QuotaDegraded is true when any quota window is exhausted or near full.
	QuotaDegraded bool
}

// evaluate applies the time windows of one candidate to the given local
// moment. A window that is active may lower the priority; a window marked
// only restricts the candidate to it.
func evaluate(target Target, local time.Time) candidateState {
	minute := local.Hour()*60 + local.Minute()
	state := candidateState{Priority: target.Priority, Active: true}
	restricted := false
	inside := false
	for i := range target.Windows {
		w := &target.Windows[i]
		if w.Only {
			restricted = true
		}
		if !w.Active(minute) {
			continue
		}
		if w.Only {
			inside = true
		}
		if w.Priority == nil {
			continue
		}
		state.Hit = true
		if *w.Priority < state.Priority {
			state.Priority = *w.Priority
			state.Window = w
			continue
		}
		if state.Window == nil {
			state.Window = w
		}
	}
	if restricted && !inside {
		// The candidate is only allowed inside one of its windows and none
		// of them is open right now.
		state.Active = false
	}
	return state
}

// lessState compares two candidates. Non-degraded candidates always rank ahead
// of quota-exhausted ones; then the effective priority decides; a candidate
// whose window is open wins a tie; the configured priority breaks the
// remaining ties.
func lessState(a candidateState, aBase int, b candidateState, bBase int) bool {
	if a.QuotaDegraded != b.QuotaDegraded {
		return !a.QuotaDegraded
	}
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	if a.Hit != b.Hit {
		return a.Hit
	}
	return aBase < bBase
}

type rankedCandidate struct {
	target Target
	state  candidateState
}

func applyQuotaState(state *candidateState, instance string, qp QuotaProvider) {
	if qp == nil {
		return
	}
	pct, exhausted, ok := qp.QuotaState(instance)
	if !ok {
		return
	}
	state.QuotaKnown = true
	state.QuotaUsedPct = pct
	state.QuotaDegraded = exhausted || pct >= DefaultQuotaDegradePct
}

// orderCandidates returns the candidates in failover order without quota or
// session awareness. Kept for unit tests.
func orderCandidates(candidates []Target, loc *time.Location, now time.Time) []Target {
	return orderCandidatesWithStrategy(candidates, loc, now, config.StrategyFailover, nil, ResolveOptions{}, nil)
}

// orderCandidatesWithStrategy orders active candidates by quota health and
// effective priority, then applies the model's routing strategy within each
// group of candidates that share the same (quotaDegraded, effectivePriority).
func orderCandidatesWithStrategy(
	candidates []Target,
	loc *time.Location,
	now time.Time,
	strategy string,
	qp QuotaProvider,
	opts ResolveOptions,
	rrCounter *atomic.Uint64,
) []Target {
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	rankedList := make([]rankedCandidate, 0, len(candidates))
	for _, target := range candidates {
		state := evaluate(target, local)
		if !state.Active {
			continue
		}
		applyQuotaState(&state, target.Instance, qp)
		rankedList = append(rankedList, rankedCandidate{target: target, state: state})
	}
	sort.SliceStable(rankedList, func(a, b int) bool {
		return lessState(rankedList[a].state, rankedList[a].target.Priority,
			rankedList[b].state, rankedList[b].target.Priority)
	})

	if strategy != "" && strategy != config.StrategyFailover && len(rankedList) > 1 {
		applyStrategyToGroups(rankedList, strategy, opts, rrCounter)
	}

	out := make([]Target, 0, len(rankedList))
	for _, item := range rankedList {
		out = append(out, item.target)
	}
	return out
}

// applyStrategyToGroups reorders each contiguous run of candidates that share
// the same (QuotaDegraded, effective Priority) according to strategy.
func applyStrategyToGroups(list []rankedCandidate, strategy string, opts ResolveOptions, rrCounter *atomic.Uint64) {
	for start := 0; start < len(list); {
		end := start + 1
		for end < len(list) &&
			list[end].state.QuotaDegraded == list[start].state.QuotaDegraded &&
			list[end].state.Priority == list[start].state.Priority {
			end++
		}
		group := list[start:end]
		if len(group) > 1 {
			switch strategy {
			case config.StrategyRoundRobin:
				if rrCounter != nil {
					shift := int((rrCounter.Add(1) - 1) % uint64(len(group)))
					rotateRanked(group, shift)
				}
			case config.StrategyLeastUsed:
				sort.SliceStable(group, func(a, b int) bool {
					if group[a].state.QuotaKnown != group[b].state.QuotaKnown {
						return group[a].state.QuotaKnown
					}
					if group[a].state.QuotaKnown && group[a].state.QuotaUsedPct != group[b].state.QuotaUsedPct {
						return group[a].state.QuotaUsedPct < group[b].state.QuotaUsedPct
					}
					return false
				})
			case config.StrategySticky:
				if opts.SessionKey != "" {
					h := fnv.New64a()
					_, _ = h.Write([]byte(group[0].target.Model))
					_, _ = h.Write([]byte{0})
					_, _ = h.Write([]byte(opts.SessionKey))
					shift := int(h.Sum64() % uint64(len(group)))
					rotateRanked(group, shift)
				}
			}
		}
		start = end
	}
}

func rotateRanked(group []rankedCandidate, shift int) {
	n := len(group)
	if n <= 1 {
		return
	}
	shift %= n
	if shift == 0 {
		return
	}
	tmp := make([]rankedCandidate, n)
	copy(tmp, group[shift:])
	copy(tmp[n-shift:], group[:shift])
	copy(group, tmp)
}

// Preview describes the routing state of one candidate at a given moment.
type Preview struct {
	// Instance is the name of the upstream instance.
	Instance string
	// Upstream is the model name the candidate sends to the instance.
	Upstream string
	// BasePriority is the configured priority of the candidate.
	BasePriority int
	// Priority is the priority that applies right now.
	Priority int
	// Active reports whether the candidate may serve the model at all.
	Active bool
	// Window describes the window that is active right now, if any.
	Window string
	// QuotaDegraded reports whether the instance is demoted due to exhausted quota.
	QuotaDegraded bool
	// QuotaUsedPct is the peak quota usage percentage when known.
	QuotaUsedPct *float64

	// hit reports whether a window with a priority is active. It breaks the
	// ties the same way it does in the routing order.
	hit bool
}

// PreviewModel returns the candidates of one model with their current
// routing state without quota awareness.
func (s *Snapshot) PreviewModel(model string, now time.Time) []Preview {
	return s.previewModelWithQuota(model, now, nil)
}

func (s *Snapshot) previewModelWithQuota(model string, now time.Time, qp QuotaProvider) []Preview {
	targets := s.ByModel[model]
	if len(targets) == 0 {
		return nil
	}
	loc := s.Location
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	out := make([]Preview, 0, len(targets))
	for _, target := range targets {
		state := evaluate(target, local)
		applyQuotaState(&state, target.Instance, qp)
		item := Preview{
			Instance:      target.Instance,
			Upstream:      target.UpstreamModel,
			BasePriority:  target.Priority,
			Priority:      state.Priority,
			Active:        state.Active,
			QuotaDegraded: state.QuotaDegraded,
			hit:           state.Hit,
		}
		if state.QuotaKnown {
			pct := state.QuotaUsedPct
			item.QuotaUsedPct = &pct
		}
		if state.Window != nil {
			item.Window = state.Window.Describe()
		}
		out = append(out, item)
	}
	strategy := s.Strategies[model]
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Active != out[b].Active {
			return out[a].Active
		}
		if out[a].QuotaDegraded != out[b].QuotaDegraded {
			return !out[a].QuotaDegraded
		}
		if out[a].Priority != out[b].Priority {
			return out[a].Priority < out[b].Priority
		}
		if strategy == config.StrategyLeastUsed && out[a].Active {
			aKnown := out[a].QuotaUsedPct != nil
			bKnown := out[b].QuotaUsedPct != nil
			if aKnown != bKnown {
				return aKnown
			}
			if aKnown && *out[a].QuotaUsedPct != *out[b].QuotaUsedPct {
				return *out[a].QuotaUsedPct < *out[b].QuotaUsedPct
			}
		}
		if out[a].hit != out[b].hit {
			return out[a].hit
		}
		return out[a].BasePriority < out[b].BasePriority
	})
	return out
}
