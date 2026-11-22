package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
)

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func providerBody(name, model string) string {
	return `{"name":"` + name + `","timeout_ms":30000,
	  "keys":["sk-secret-1234"],
	  "endpoints":[{"name":"openai","protocol":"openai",
	    "base_url":"http://127.0.0.1:9/v1","models":{"` + model + `":"` + model + `"}}]}`
}

func decodeProvider(t *testing.T, resp *http.Response) providerView {
	t.Helper()
	defer resp.Body.Close()
	var view providerView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestProviderCRUD(t *testing.T) {
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"})

	// Create
	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/providers", providerBody("ui-provider", "m1"))
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("create = %d %s", resp.StatusCode, body)
	}
	created := decodeProvider(t, resp)
	if created.KeyCount != 1 {
		t.Fatalf("key count = %d", created.KeyCount)
	}
	if len(created.KeyHints) != 1 || created.KeyHints[0] != "****1234" {
		t.Fatalf("key hints = %v", created.KeyHints)
	}
	if created.ID == 0 {
		t.Fatal("missing id")
	}

	// List
	listResp := adminRequest(t, http.MethodGet, gw.URL+"/admin/api/providers", "")
	defer listResp.Body.Close()
	var list []providerView
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "ui-provider" {
		t.Fatalf("list = %+v", list)
	}

	// Update without keys preserves them.
	update := `{"name":"ui-provider","timeout_ms":60000,
	  "endpoints":[{"name":"openai","protocol":"openai",
	    "base_url":"http://127.0.0.1:9/v1","models":{"m2":"m2"}}]}`
	resp = adminRequest(t, http.MethodPut, gw.URL+"/admin/api/providers/"+itoa(created.ID), update)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("update = %d %s", resp.StatusCode, body)
	}
	updated := decodeProvider(t, resp)
	if updated.KeyCount != 1 {
		t.Fatalf("keys were dropped on update: %d", updated.KeyCount)
	}
	if _, ok := updated.Endpoints[0].Models["m2"]; !ok {
		t.Fatalf("endpoint update lost: %+v", updated.Endpoints)
	}

	// Duplicate name is rejected.
	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/providers", providerBody("ui-provider", "m3"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate name = %d", resp.StatusCode)
	}

	// Duplicate model on the same protocol is rejected.
	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/providers", providerBody("other", "m2"))
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate model = %d %s", resp.StatusCode, body)
	}

	// Delete
	resp = adminRequest(t, http.MethodDelete, gw.URL+"/admin/api/providers/"+itoa(created.ID), "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	resp = adminRequest(t, http.MethodGet, gw.URL+"/admin/api/providers", "")
	defer resp.Body.Close()
	list = nil
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("provider not deleted: %+v", list)
	}
}
