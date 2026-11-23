package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	// The gateway ships as a single binary and must not depend on the host
	// having a zoneinfo database. Embedding the table keeps IANA timezone
	// names working on bare containers and release archives.
	_ "time/tzdata"
)

// DefaultTimezone is the timezone every time window is evaluated in when the
// configuration does not name one.
const DefaultTimezone = "Asia/Shanghai"

// Schedule holds the global settings that decide when a candidate is used.
type Schedule struct {
	// Timezone is the name of the timezone every time window is evaluated
	// in. It accepts "UTC", an IANA name such as "Asia/Shanghai" and a fixed
	// offset such as "+08:00".
	Timezone string `yaml:"timezone"`

	// Location is the parsed timezone. It is filled in while normalizing
	// the configuration and is never read from YAML.
	Location *time.Location `yaml:"-"`
}

// TimeWindow is a time range attached to one model candidate. Inside the
// range the candidate may take over another priority, and outside it the
// candidate may be switched off entirely.
type TimeWindow struct {
	// Start is the begin of the range as a 24 hour clock time ("HH:MM").
	Start string `yaml:"start" json:"start"`
	// End is the end of the range as a 24 hour clock time ("HH:MM"). A start
	// later than the end means the range wraps around midnight.
	End string `yaml:"end" json:"end"`
	// Priority is the priority the candidate takes inside the range. It is
	// optional: a window without it only restricts availability.
	Priority *int `yaml:"priority" json:"priority,omitempty"`
	// Only restricts the candidate to this range: outside it the candidate
	// is not used at all.
	Only bool `yaml:"only" json:"only,omitempty"`
}

// EffectiveWindow is a compiled time window.
type EffectiveWindow struct {
	// StartMinute is the begin of the range in minutes since midnight.
	StartMinute int
	// EndMinute is the end of the range in minutes since midnight.
	EndMinute int
	// Priority is the priority inside the range, if any.
	Priority *int
	// Only marks a window that restricts the candidate to it.
	Only bool
}

// Active reports whether the range covers the given minute of the day. The
// range is half open: it starts at StartMinute and ends at EndMinute, so
// consecutive ranges do not overlap at the seam.
func (w EffectiveWindow) Active(minute int) bool {
	if w.StartMinute < w.EndMinute {
		return minute >= w.StartMinute && minute < w.EndMinute
	}
	// The range wraps around midnight.
	return minute >= w.StartMinute || minute < w.EndMinute
}

// Describe renders the range as "start-end", for the console and the logs.
func (w EffectiveWindow) Describe() string {
	return formatClock(w.StartMinute) + "-" + formatClock(w.EndMinute)
}

// formatClock renders minutes since midnight as a 24 hour clock time.
func formatClock(minute int) string {
	return fmt.Sprintf("%02d:%02d", minute/60, minute%60)
}

// fixedOffsetPattern matches the "+08:00" and "-0530" forms.
var fixedOffsetPattern = regexp.MustCompile(`^([+-])(\d{1,2}):?(\d{2})$`)

// resolveLocation turns a timezone name into a location. An empty name falls
// back to the default timezone.
func resolveLocation(name string) (*time.Location, error) {
	raw := strings.TrimSpace(name)
	if raw == "" {
		raw = DefaultTimezone
	}
	if strings.EqualFold(raw, "UTC") || strings.EqualFold(raw, "Z") {
		return time.UTC, nil
	}
	if loc, ok := fixedOffset(raw); ok {
		return loc, nil
	}
	loc, err := time.LoadLocation(raw)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q: use \"UTC\", an IANA name such as %q or an offset such as \"+08:00\"",
			name, DefaultTimezone)
	}
	return loc, nil
}

// fixedOffset parses a numeric UTC offset into a fixed location.
func fixedOffset(raw string) (*time.Location, bool) {
	m := fixedOffsetPattern.FindStringSubmatch(raw)
	if m == nil {
		return nil, false
	}
	hours, err := strconv.Atoi(m[2])
	if err != nil || hours > 23 {
		return nil, false
	}
	minutes, err := strconv.Atoi(m[3])
	if err != nil || minutes > 59 {
		return nil, false
	}
	offset := hours*3600 + minutes*60
	if m[1] == "-" {
		offset = -offset
	}
	name := fmt.Sprintf("%s%02d:%02d", m[1], hours, minutes)
	return time.FixedZone(name, offset), true
}

// clockPattern matches the strict "HH:MM" form of a 24 hour clock time.
var clockPattern = regexp.MustCompile(`^([0-9]{2}):([0-9]{2})$`)

// parseClock parses a 24 hour clock time into minutes since midnight.
// "24:00" is accepted as the end of a day.
func parseClock(raw string) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("time is required")
	}
	m := clockPattern.FindStringSubmatch(value)
	if m == nil {
		return 0, fmt.Errorf("invalid time %q: want HH:MM", raw)
	}
	hours, _ := strconv.Atoi(m[1])
	minutes, _ := strconv.Atoi(m[2])
	if hours > 24 {
		return 0, fmt.Errorf("invalid time %q: want HH:MM", raw)
	}
	if minutes > 59 {
		return 0, fmt.Errorf("invalid time %q: want HH:MM", raw)
	}
	if hours == 24 && minutes != 0 {
		return 0, fmt.Errorf("invalid time %q: 24:00 is the only valid time with hour 24", raw)
	}
	return hours*60 + minutes, nil
}

// compileWindows validates the windows of one candidate and returns their
// compiled form.
func compileWindows(owner string, windows []TimeWindow) ([]EffectiveWindow, error) {
	out := make([]EffectiveWindow, 0, len(windows))
	for i := range windows {
		w := &windows[i]
		start, err := parseClock(w.Start)
		if err != nil {
			return nil, fmt.Errorf("%s: window %d: %w", owner, i+1, err)
		}
		if start >= 24*60 {
			return nil, fmt.Errorf("%s: window %d: start must be earlier than 24:00", owner, i+1)
		}
		end, err := parseClock(w.End)
		if err != nil {
			return nil, fmt.Errorf("%s: window %d: %w", owner, i+1, err)
		}
		if start == end {
			return nil, fmt.Errorf("%s: window %d: start and end must differ", owner, i+1)
		}
		if w.Priority != nil && *w.Priority < 0 {
			return nil, fmt.Errorf("%s: window %d: priority must not be negative", owner, i+1)
		}
		if w.Priority == nil && !w.Only {
			return nil, fmt.Errorf("%s: window %d: a window must set a priority or restrict the candidate with only",
				owner, i+1)
		}
		out = append(out, EffectiveWindow{
			StartMinute: start,
			EndMinute:   end,
			Priority:    w.Priority,
			Only:        w.Only,
		})
	}
	return out, nil
}
