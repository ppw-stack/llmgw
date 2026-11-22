package server

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	"github.com/pw/llmgw/internal/auth"
)

func adminClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func adminDo(t *testing.T, c *http.Client, method, url, body string) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

func loginAdmin(t *testing.T, c *http.Client, base string) {
	t.Helper()
	status, body := adminDo(t, c, http.MethodPost, base+"/admin/api/login",
		`{"username":"admin","password":"s3cret-password-123"}`)
	if status != http.StatusOK {
		t.Fatalf("login status %d: %s", status, body)
	}
}

func TestAdminLoginFlow(t *testing.T) {
	gw, st, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"up-1"})
	hash, err := auth.HashPassword("s3cret-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser("admin", hash); err != nil {
		t.Fatal(err)
	}

	c := adminClient(t)
	status, body := adminDo(t, c, http.MethodGet, gw.URL+"/admin/api/me", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /me = %d %s", status, body)
	}

	loginAdmin(t, c, gw.URL)

	status, body = adminDo(t, c, http.MethodGet, gw.URL+"/admin/api/me", "")
	if status != http.StatusOK || !strings.Contains(body, `"admin"`) {
		t.Fatalf("/me = %d %s", status, body)
	}

	status, body = adminDo(t, c, http.MethodGet, gw.URL+"/admin/api/keys", "")
	if status != http.StatusOK {
		t.Fatalf("keys with session = %d %s", status, body)
	}

	status, body = adminDo(t, c, http.MethodPost, gw.URL+"/admin/api/logout", "")
	if status != http.StatusNoContent {
		t.Fatalf("logout = %d %s", status, body)
	}
	status, _ = adminDo(t, c, http.MethodGet, gw.URL+"/admin/api/me", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("after logout /me = %d", status)
	}
}

func TestAdminLoginRateLimited(t *testing.T) {
	gw, st, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"up-1"})
	hash, _ := auth.HashPassword("s3cret-password-123")
	if _, err := st.CreateUser("admin", hash); err != nil {
		t.Fatal(err)
	}
	c := adminClient(t)
	for i := 0; i < 10; i++ {
		status, _ := adminDo(t, c, http.MethodPost, gw.URL+"/admin/api/login",
			`{"username":"admin","password":"wrong"}`)
		if status != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d", i+1, status)
		}
	}
	status, body := adminDo(t, c, http.MethodPost, gw.URL+"/admin/api/login",
		`{"username":"admin","password":"wrong"}`)
	if status != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after limiter, got %d %s", status, body)
	}
}

func TestAdminPasswordChangeInvalidatesSessions(t *testing.T) {
	gw, st, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"up-1"})
	hash, _ := auth.HashPassword("s3cret-password-123")
	if _, err := st.CreateUser("admin", hash); err != nil {
		t.Fatal(err)
	}
	c := adminClient(t)
	loginAdmin(t, c, gw.URL)

	status, body := adminDo(t, c, http.MethodPost, gw.URL+"/admin/api/password",
		`{"old_password":"s3cret-password-123","new_password":"another-strong-pass"}`)
	if status != http.StatusNoContent {
		t.Fatalf("password change = %d %s", status, body)
	}
	status, _ = adminDo(t, c, http.MethodGet, gw.URL+"/admin/api/me", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("session should be invalidated, got %d", status)
	}

	c2 := adminClient(t)
	status, body = adminDo(t, c2, http.MethodPost, gw.URL+"/admin/api/login",
		`{"username":"admin","password":"another-strong-pass"}`)
	if status != http.StatusOK {
		t.Fatalf("login with new password = %d %s", status, body)
	}
}

func TestAuditLog(t *testing.T) {
	gw, st, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"up-1"})
	hash, _ := auth.HashPassword("s3cret-password-123")
	if _, err := st.CreateUser("admin", hash); err != nil {
		t.Fatal(err)
	}
	c := adminClient(t)
	loginAdmin(t, c, gw.URL)
	if status, body := adminDo(t, c, http.MethodPost, gw.URL+"/admin/api/keys",
		`{"name":"laptop"}`); status != http.StatusCreated {
		t.Fatalf("create key = %d %s", status, body)
	}
	status, body := adminDo(t, c, http.MethodGet, gw.URL+"/admin/api/audit", "")
	if status != http.StatusOK {
		t.Fatalf("audit = %d %s", status, body)
	}
	if !strings.Contains(body, `"login"`) || !strings.Contains(body, `"key.create"`) {
		t.Fatalf("audit entries missing: %s", body)
	}
}

func TestSecurityHeaders(t *testing.T) {
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"up-1"})
	resp, err := http.Get(gw.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff = %q", got)
	}
	if got := resp.Header.Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") {
		t.Fatalf("csp = %q", got)
	}
	if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("frame options = %q", got)
	}
}

func TestLegacyAdminTokenStillWorks(t *testing.T) {
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"up-1"})
	req, _ := http.NewRequest(http.MethodGet, gw.URL+"/admin/api/keys", nil)
	req.Header.Set("Authorization", "Bearer admin-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy token status = %d", resp.StatusCode)
	}
}
