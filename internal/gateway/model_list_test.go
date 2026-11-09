package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestModelListFiltersEachKeyWithoutFilteringSharedCatalog(t *testing.T) {
	h := newHarness(t, nil)
	var catalogCalls atomic.Int32
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		catalogCalls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v1/codar/models" {
			t.Error("unexpected catalog request", r.Method, r.URL.Path)
		}
		jsonResponse(w, 200, M{"data": []any{M{"id": "alpha"}, M{"id": "alpha-v2"}, M{"id": "beta"}}})
	}))
	t.Cleanup(catalog.Close)
	h.app.cfg.APIBase = catalog.URL

	list := func(t *testing.T, token, header string, want []string) {
		t.Helper()
		req, err := http.NewRequest("GET", h.server.URL+"/v1/models", nil)
		if err != nil {
			t.Fatal(err)
		}
		if header == "Authorization" {
			req.Header.Set(header, "Bearer "+token)
		} else {
			req.Header.Set(header, token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("model list: HTTP %d: %s", resp.StatusCode, raw)
		}
		out := decode(t, raw)
		data, ok := out["data"].([]any)
		if out["object"] != "list" || !ok {
			t.Fatalf("expected a model list with a JSON array: %s", raw)
		}
		got := []string{}
		for _, entry := range data {
			model := obj(entry)
			if model["object"] != "model" || model["owned_by"] != "codar" {
				t.Fatal("invalid model entry", model)
			}
			got = append(got, text(model["id"]))
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("model IDs: got %v, want %v", got, want)
		}
	}

	unrestricted := []string{"alpha", "alpha-v2", "beta"}
	for _, model := range codarImageModels {
		unrestricted = append(unrestricted, model.id)
	}
	var limited M
	for _, tc := range []struct {
		name   string
		models []any
		want   []string
	}{
		{"single model", []any{"alpha"}, []string{"alpha"}},
		{"unrestricted", []any{}, unrestricted},
		{"different key", []any{"beta"}, []string{"beta"}},
		{"multiple models", []any{"beta", "alpha"}, []string{"alpha", "beta"}},
		{"image model only", []any{defaultCodarImageModel}, []string{defaultCodarImageModel}},
		{"chat and image models", []any{"beta", defaultCodarImageModel}, []string{"beta", defaultCodarImageModel}},
		{"partially outside catalog", []any{"alpha", "missing"}, []string{"alpha"}},
		{"no intersection", []any{"missing"}, []string{}},
		{"exact IDs only", []any{"Alpha", "alp", "beta "}, []string{"beta"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := h.post(t, "/keys", M{"action": "create", "name": tc.name, "models": tc.models})
			if tc.name == "single model" {
				limited = key
			}
			for _, header := range []string{"Authorization", "x-api-key"} {
				list(t, text(key["token"]), header, tc.want)
			}
		})
	}

	// An existing cached catalog must not delay permission changes or be narrowed
	// by a previous caller's model restrictions.
	h.post(t, "/keys", M{"action": "update", "id": obj(limited["key"])["id"], "models": []any{"beta"}})
	list(t, text(limited["token"]), "Authorization", []string{"beta"})
	list(t, h.key, "Authorization", unrestricted)
	resp, raw := h.request(t, "GET", "/catalog", nil, false)
	if resp.StatusCode != 200 || len(arr(decode(t, raw)["models"])) != len(unrestricted) {
		t.Fatalf("key restrictions changed the administrator's catalog: %d %s", resp.StatusCode, raw)
	}
	if catalogCalls.Load() != 1 {
		t.Fatal("model lists did not share the global catalog cache", catalogCalls.Load())
	}
}

func TestModelListRequiresValidKeyEvenWithAdminSession(t *testing.T) {
	h := newHarness(t, nil)
	key := h.post(t, "/keys", M{"action": "create", "name": "disabled", "models": []any{"auto"}})
	h.post(t, "/keys", M{"action": "update", "id": obj(key["key"])["id"], "enabled": false})
	for _, token := range []string{"", "invalid-key", text(key["token"])} {
		req, err := http.NewRequest("GET", h.server.URL+"/v1/models", nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := h.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatal("model listing did not reject an absent, invalid or disabled key", resp.StatusCode)
		}
	}
	h.callsMu.Lock()
	defer h.callsMu.Unlock()
	if len(h.calls) != 0 {
		t.Fatal("unauthorized model listing fetched the upstream catalog", h.calls)
	}
}
