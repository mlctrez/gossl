package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kardianos/service"
)

func newTestWeb(t *testing.T) (*WebHandler, *EndpointStore) {
	t.Helper()
	store, err := NewEndpointStore(filepath.Join(t.TempDir(), "endpoints.json"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	svc := &Service{}
	svc.Logger(service.ConsoleLogger)
	return NewWebHandler(store, nil, svc), store
}

func TestWeb_Add_PersistsRequireCloudFront(t *testing.T) {
	h, store := newTestWeb(t)

	form := url.Values{}
	form.Set("host", "app.example.com")
	form.Set("url", "http://10.0.0.1:9000")
	form.Set("skipToken", "true")
	form.Set("requireCloudFront", "true")
	req := httptest.NewRequest(http.MethodPost, "/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d body %s", rr.Code, rr.Body.String())
	}
	all := store.All()
	if len(all) != 1 || !all[0].RequireCloudFront || !all[0].SkipToken {
		t.Fatalf("expected both flags stored, got %+v", all)
	}
}

func TestWeb_Add_ClearsRequireCloudFront(t *testing.T) {
	h, store := newTestWeb(t)
	if err := store.Add(Endpoint{
		Host: "app.example.com", URL: "http://10.0.0.1:9000", RequireCloudFront: true,
	}); err != nil {
		t.Fatal(err)
	}

	form := url.Values{}
	form.Set("host", "app.example.com")
	form.Set("url", "http://10.0.0.1:9000")
	req := httptest.NewRequest(http.MethodPost, "/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", rr.Code)
	}
	all := store.All()
	if len(all) != 1 || all[0].RequireCloudFront {
		t.Fatalf("expected requireCloudFront cleared, got %+v", all)
	}
}

func TestWeb_Edit_PrefillsRequireCloudFront(t *testing.T) {
	h, _ := newTestWeb(t)

	form := url.Values{}
	form.Set("host", "app.example.com")
	form.Set("url", "http://10.0.0.1:9000")
	form.Set("skipToken", "false")
	form.Set("requireCloudFront", "true")
	req := httptest.NewRequest(http.MethodPost, "/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `name="requireCloudFront" value="true" checked`) {
		t.Fatalf("edit form did not check requireCloudFront:\n%s", rr.Body.String())
	}
}

func TestWeb_List_ShowsCloudFrontColumn(t *testing.T) {
	h, store := newTestWeb(t)
	if err := store.Add(Endpoint{
		Host: "app.example.com", URL: "http://10.0.0.1:9000", RequireCloudFront: true,
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "CloudFront") || !strings.Contains(body, ">yes<") {
		t.Fatalf("list did not show the CloudFront flag:\n%s", body)
	}
}
