package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExampleConfigLoads keeps the shipped example valid: a broken example
// is the first thing a new user runs into.
func TestExampleConfigLoads(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("example config not found: %v", err)
	}
	// The example references environment variables; an empty value is fine
	// for the load itself.
	t.Setenv("MINIMAX_KEY", "sk-example")
	t.Setenv("MINIMAX_KEY_BACKUP", "sk-example")
	t.Setenv("GLM_KEY", "sk-example")
	t.Setenv("DEEPSEEK_KEY", "sk-example")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load example config: %v", err)
	}
	if cfg.Schedule.Location == nil {
		t.Fatal("the schedule timezone is not resolved")
	}
	if cfg.Schedule.Location.String() != "Asia/Shanghai" {
		t.Fatalf("timezone = %q", cfg.Schedule.Location.String())
	}
	var found bool
	for _, m := range cfg.Models {
		for _, cand := range m.Instances {
			for _, w := range cand.Windows {
				found = true
				if w.Start == "" || w.End == "" {
					t.Fatalf("model %q: window = %+v", m.Name, w)
				}
			}
		}
	}
	if !found {
		t.Fatal("the example should show at least one time window")
	}
}
