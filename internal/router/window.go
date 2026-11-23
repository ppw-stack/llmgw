package router

import (
	"sort"
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

// lessState compares two candidates. The effective priority decides; a
// candidate whose window is open wins a tie, because the window was
// configured to take over exactly then; the configured priority breaks the
// remaining ties.
func lessState(a candidateState, aBase int, b candidateState, bBase int) bool {
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	if a.Hit != b.Hit {
		return a.Hit
	}
	return aBase < bBase
}

// orderCandidates returns the candidates in the order they should be tried:
// the effective priority decides, the configured order breaks the ties. The
// candidates that are outside their time window are dropped.
func orderCandidates(candidates []Target, loc *time.Location, now time.Time) []Target {
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	type ranked struct {
		target Target
		state  candidateState
	}
	rankedList := make([]ranked, 0, len(candidates))
	for _, target := range candidates {
		state := evaluate(target, local)
		if !state.Active {
			continue
		}
		rankedList = append(rankedList, ranked{target: target, state: state})
	}
	sort.SliceStable(rankedList, func(a, b int) bool {
		return lessState(rankedList[a].state, rankedList[a].target.Priority,
			rankedList[b].state, rankedList[b].target.Priority)
	})
	out := make([]Target, 0, len(rankedList))
	for _, item := range rankedList {
		out = append(out, item.target)
	}
	return out
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
	// Active reports whether the candidate may serve the model right now.
	Active bool
	// Window describes the window that is active right now, if any.
	Window string

	// hit reports whether a window with a priority is active. It breaks the
	// ties the same way it does in the routing order.
	hit bool
}

// PreviewModel returns the candidates of one model with their current
// routing state. The candidates keep the order they would be tried in.
func (s *Snapshot) PreviewModel(model string, now time.Time) []Preview {
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
		item := Preview{
			Instance:     target.Instance,
			Upstream:     target.UpstreamModel,
			BasePriority: target.Priority,
			Priority:     state.Priority,
			Active:       state.Active,
			hit:          state.Hit,
		}
		if state.Window != nil {
			item.Window = state.Window.Describe()
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Active != out[b].Active {
			// Inactive candidates are listed last: they are configured but
			// not serving the model right now.
			return out[a].Active
		}
		if out[a].Priority != out[b].Priority {
			return out[a].Priority < out[b].Priority
		}
		if out[a].hit != out[b].hit {
			return out[a].hit
		}
		return out[a].BasePriority < out[b].BasePriority
	})
	return out
}
