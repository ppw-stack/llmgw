package config

import (
	"strings"
	"testing"
	"time"
)

func TestResolveLocation(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty falls back to the default", "", DefaultTimezone},
		{"utc", "UTC", "UTC"},
		{"iana name", "Asia/Shanghai", "Asia/Shanghai"},
		{"fixed offset", "+08:00", "+08:00"},
		{"fixed offset without colon", "-0530", "-05:30"},
		{"fixed offset with one digit hour", "+8:00", "+08:00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loc, err := resolveLocation(tc.in)
			if err != nil {
				t.Fatalf("resolve %q: %v", tc.in, err)
			}
			if loc.String() != tc.want {
				t.Fatalf("location = %q, want %q", loc.String(), tc.want)
			}
		})
	}
}

func TestResolveLocationRejectsUnknownZone(t *testing.T) {
	if _, err := resolveLocation("Mars/Olympus"); err == nil {
		t.Fatal("unknown timezone should be rejected")
	} else if !strings.Contains(err.Error(), "unknown timezone") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseClock(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{in: "00:00", want: 0},
		{in: "08:30", want: 510},
		{in: "23:59", want: 1439},
		{in: "24:00", want: 1440},
		{in: "7:5", wantErr: true},
		{in: "25:00", wantErr: true},
		{in: "24:01", wantErr: true},
		{in: "12:60", wantErr: true},
		{in: "1200", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tc := range cases {
		got, err := parseClock(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("parse %q should fail", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parse %q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("parse %q = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeWindows(t *testing.T) {
	low := 0
	windows := []TimeWindow{
		{Start: "00:00", End: "08:00", Priority: &low},
		{Start: "22:00", End: "06:00", Only: true},
	}
	compiled, err := compileWindows("model \"m\": instance \"i\"", windows)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(compiled) != 2 {
		t.Fatalf("compiled = %+v", compiled)
	}
	if compiled[0].StartMinute != 0 || compiled[0].EndMinute != 480 {
		t.Fatalf("first window = %+v", compiled[0])
	}
	// A start later than the end wraps around midnight.
	if compiled[1].StartMinute != 1320 || compiled[1].EndMinute != 360 {
		t.Fatalf("second window = %+v", compiled[1])
	}
	if !compiled[1].Only {
		t.Fatalf("second window = %+v", compiled[1])
	}
	// The clock times are written back in their canonical form.
	if windows[0].Start != "00:00" || windows[0].End != "08:00" {
		t.Fatalf("windows = %+v", windows)
	}
}

func TestNormalizeWindowsRejectsBrokenRanges(t *testing.T) {
	negative := -1
	cases := []struct {
		name   string
		window TimeWindow
	}{
		{"start equals end", TimeWindow{Start: "08:00", End: "08:00", Priority: &negative}},
		{"broken start", TimeWindow{Start: "8:00", End: "09:00", Priority: &negative}},
		{"negative priority", TimeWindow{Start: "08:00", End: "09:00", Priority: &negative}},
		{"start at the end of the day", TimeWindow{Start: "24:00", End: "09:00", Priority: &negative}},
		{"window without meaning", TimeWindow{Start: "08:00", End: "09:00"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := compileWindows("owner", []TimeWindow{tc.window}); err == nil {
				t.Fatal("broken window should be rejected")
			}
		})
	}
}

func TestEffectiveWindowActive(t *testing.T) {
	day := EffectiveWindow{StartMinute: 0, EndMinute: 480}
	night := EffectiveWindow{StartMinute: 1320, EndMinute: 360}
	cases := []struct {
		window EffectiveWindow
		minute int
		want   bool
	}{
		{window: day, minute: 0, want: true},
		{window: day, minute: 479, want: true},
		{window: day, minute: 480, want: false},
		{window: day, minute: 1439, want: false},
		{window: night, minute: 1320, want: true},
		{window: night, minute: 1439, want: true},
		{window: night, minute: 0, want: true},
		{window: night, minute: 359, want: true},
		{window: night, minute: 360, want: false},
	}
	for _, tc := range cases {
		if got := tc.window.Active(tc.minute); got != tc.want {
			t.Fatalf("window %s at %d = %v, want %v",
				tc.window.Describe(), tc.minute, got, tc.want)
		}
	}
}

func TestNormalizeModelKeepsWindows(t *testing.T) {
	low := 0
	model := Model{
		Name: "m",
		Instances: []ModelInstance{
			{Name: "a", Upstream: "u", Priority: 5, Windows: []TimeWindow{
				{Start: "00:00", End: "08:00", Priority: &low},
			}},
		},
	}
	if err := NormalizeModel(&model); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(model.Instances[0].Windows) != 1 {
		t.Fatalf("windows = %+v", model.Instances[0].Windows)
	}
	// The base priority decides the configured order, the windows only
	// decide the order at request time.
	if model.Instances[0].Priority != 5 {
		t.Fatalf("priority = %d", model.Instances[0].Priority)
	}
}

func TestEffectiveCompilesWindows(t *testing.T) {
	low := 0
	cfg := &Config{
		Instances: []Instance{
			{
				Name:      "a",
				APIKey:    "k",
				Endpoints: []Endpoint{{Protocol: ProtocolOpenAI, BaseURL: "https://a/v1"}},
				Models:    []string{"u"},
			},
		},
		Models: []Model{
			{
				Name: "m",
				Instances: []ModelInstance{
					{Name: "a", Upstream: "u", Priority: 5, Windows: []TimeWindow{
						{Start: "00:00", End: "08:00", Priority: &low},
					}},
				},
			},
		},
	}
	eff, err := cfg.Effective()
	if err != nil {
		t.Fatalf("effective: %v", err)
	}
	windows := eff.Models[0].Instances[0].Windows
	if len(windows) != 1 || windows[0].EndMinute != 480 {
		t.Fatalf("windows = %+v", windows)
	}
}

func TestLoadRejectsUnknownTimezone(t *testing.T) {
	path := writeConfig(t, `
schedule:
  timezone: "Mars/Olympus"
instances:
  - name: a
    api_key: "k"
    endpoints:
      - protocol: openai
        base_url: https://a/v1
    models: [u]
models:
  - name: m
    instances:
      - name: a
        upstream: u
`)
	if _, err := Load(path); err == nil {
		t.Fatal("unknown timezone should fail the load")
	}
}

func TestLoadAcceptsWindows(t *testing.T) {
	path := writeConfig(t, `
schedule:
  timezone: "Asia/Shanghai"
instances:
  - name: a
    api_key: "k"
    endpoints:
      - protocol: openai
        base_url: https://a/v1
    models: [u]
models:
  - name: m
    instances:
      - name: a
        upstream: u
        priority: 5
        windows:
          - start: "00:00"
            end: "08:00"
            priority: 0
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Schedule.Location == nil {
		t.Fatal("location is not resolved")
	}
	if cfg.Schedule.Location.String() != "Asia/Shanghai" {
		t.Fatalf("location = %q", cfg.Schedule.Location.String())
	}
	windows := cfg.Models[0].Instances[0].Windows
	if len(windows) != 1 || windows[0].Start != "00:00" || windows[0].End != "08:00" {
		t.Fatalf("windows = %+v", windows)
	}
	if windows[0].Priority == nil || *windows[0].Priority != 0 {
		t.Fatalf("priority = %+v", windows[0].Priority)
	}
}

func TestScheduleDefaultTimezone(t *testing.T) {
	cfg := defaults()
	if cfg.Schedule.Timezone != DefaultTimezone {
		t.Fatalf("timezone = %q", cfg.Schedule.Timezone)
	}
	loc, err := resolveLocation(cfg.Schedule.Timezone)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// The default zone is fixed to UTC+8 without daylight saving time.
	moment := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	if got := moment.In(loc).Hour(); got != 20 {
		t.Fatalf("hour = %d", got)
	}
}
