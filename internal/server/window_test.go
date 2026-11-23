package server

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/pw/llmgw/internal/config"
)

// upstreamKey returns the upstream key used by the window tests. The
// environment variable injects a real key; the fallback is an obviously
// synthetic placeholder so no credential is stored in the repository.
func upstreamKey() string {
	return testAPIKey("TEST_WINDOW_UPSTREAM_KEY", "0001")
}

func TestModelWindowsInAdminAPI(t *testing.T) {
	t.Parallel()
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{upstreamKey()})
	apiKey := testAPIKey("TEST_WINDOW_API_KEY", "9999")

	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances",
		instanceBody("cheap", apiKey))
	if resp.StatusCode != http.StatusCreated {
		body := readAll(t, resp)
		t.Fatalf("create instance = %d %s", resp.StatusCode, body)
	}
	resp.Body.Close()

	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/models",
		`{"name":"window-model","strategy":"failover",
		  "instances":[{"instance":"cheap","upstream":"gpt-upstream","priority":5,
		    "windows":[{"start":"00:00","end":"08:00","priority":0}]}]}`)
	if resp.StatusCode != http.StatusCreated {
		body := readAll(t, resp)
		t.Fatalf("create model = %d %s", resp.StatusCode, body)
	}
	created := decodeModel(t, resp)

	if len(created.Instances) != 1 {
		t.Fatalf("candidates = %+v", created.Instances)
	}
	windows := created.Instances[0].Windows
	if len(windows) != 1 {
		t.Fatalf("windows = %+v", windows)
	}
	if windows[0].Start != "00:00" || windows[0].End != "08:00" {
		t.Fatalf("window = %+v", windows[0])
	}
	if windows[0].Priority == nil || *windows[0].Priority != 0 {
		t.Fatalf("window priority = %+v", windows[0].Priority)
	}
	if created.Timezone == "" {
		t.Fatal("the model view must report the schedule timezone")
	}
	if len(created.Effective) != 1 || created.Effective[0].Instance != "cheap" {
		t.Fatalf("effective candidates = %+v", created.Effective)
	}
	if created.Effective[0].BasePriority != 5 {
		t.Fatalf("base priority = %+v", created.Effective[0])
	}
}

func TestModelWindowsAreValidated(t *testing.T) {
	t.Parallel()
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{upstreamKey()})
	apiKey := testAPIKey("TEST_WINDOW_API_KEY", "9999")

	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances",
		instanceBody("cheap", apiKey))
	resp.Body.Close()

	cases := []struct {
		name string
		body string
	}{
		{
			name: "broken clock time",
			body: `{"name":"bad-window",
			  "instances":[{"instance":"cheap","upstream":"gpt-upstream","windows":[{"start":"25:00","end":"08:00","priority":0}]}]}`,
		},
		{
			name: "equal start and end",
			body: `{"name":"bad-window",
			  "instances":[{"instance":"cheap","upstream":"gpt-upstream","windows":[{"start":"08:00","end":"08:00","priority":0}]}]}`,
		},
		{
			name: "window without meaning",
			body: `{"name":"bad-window",
			  "instances":[{"instance":"cheap","upstream":"gpt-upstream","windows":[{"start":"08:00","end":"09:00"}]}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/models", tc.body)
			body := readAll(t, resp)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d %s", resp.StatusCode, body)
			}
		})
	}
}

func TestModelWindowTakesOverNow(t *testing.T) {
	t.Parallel()
	loc, err := time.LoadLocation("UTC")
	if err != nil {
		t.Fatal(err)
	}
	gw, _, _ := setupConfig(t, nil, func(w http.ResponseWriter, r *http.Request) {},
		[]string{upstreamKey()}, func(c *config.Config) {
			c.Schedule = config.Schedule{Timezone: "UTC", Location: loc}
		})

	// The window is open right now, so the candidate takes over with the
	// priority of the window.
	now := time.Now().UTC()
	start := now.Add(-time.Hour).Format("15:04")
	end := now.Add(time.Hour).Format("15:04")
	body := fmt.Sprintf(`{"name":"open-model","strategy":"failover",
	  "instances":[{"instance":"test","upstream":"gpt-upstream","priority":7,
	    "windows":[{"start":%q,"end":%q,"priority":0}]}]}`, start, end)

	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/models", body)
	if resp.StatusCode != http.StatusCreated {
		payload := readAll(t, resp)
		t.Fatalf("create model = %d %s", resp.StatusCode, payload)
	}
	created := decodeModel(t, resp)

	if len(created.Effective) != 1 {
		t.Fatalf("effective candidates = %+v", created.Effective)
	}
	effective := created.Effective[0]
	if effective.Instance != "test" {
		t.Fatalf("effective candidate = %+v", effective)
	}
	if effective.Priority != 0 || effective.BasePriority != 7 {
		t.Fatalf("effective candidate = %+v", effective)
	}
	if !effective.Active {
		t.Fatalf("the candidate must be active: %+v", effective)
	}
	if effective.Window == "" {
		t.Fatalf("the active window must be reported: %+v", effective)
	}
}

func TestModelOutsideItsWindowIsUnavailable(t *testing.T) {
	t.Parallel()
	loc, err := time.LoadLocation("UTC")
	if err != nil {
		t.Fatal(err)
	}
	gw, _, key := setupConfig(t, nil, func(w http.ResponseWriter, r *http.Request) {},
		[]string{upstreamKey()}, func(c *config.Config) {
			c.Schedule = config.Schedule{Timezone: "UTC", Location: loc}
		})

	// The window closed an hour ago, so the candidate may not serve the
	// model right now.
	now := time.Now().UTC()
	start := now.Add(-2 * time.Hour).Format("15:04")
	end := now.Add(-1 * time.Hour).Format("15:04")
	body := fmt.Sprintf(`{"name":"closed-model","strategy":"failover",
	  "instances":[{"instance":"test","upstream":"gpt-upstream","priority":0,
	    "windows":[{"start":%q,"end":%q,"only":true}]}]}`, start, end)

	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/models", body)
	if resp.StatusCode != http.StatusCreated {
		payload := readAll(t, resp)
		t.Fatalf("create model = %d %s", resp.StatusCode, payload)
	}
	resp.Body.Close()

	resp = post(t, gw.URL+"/openai/v1/chat/completions", key,
		`{"model":"closed-model","messages":[{"role":"user","content":"hi"}]}`)
	payload := readAll(t, resp)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d %s", resp.StatusCode, payload)
	}
}
