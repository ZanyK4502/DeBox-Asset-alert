package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/config"
)

func TestHealth(t *testing.T) {
	server := httptest.NewServer(New(testConfig(t)))
	defer server.Close()

	response, err := http.Get(server.URL + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	var body struct {
		OK          bool   `json:"ok"`
		App         string `json:"app"`
		Environment string `json:"environment"`
		ReceiveMode string `json:"receive_mode"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if !body.OK || body.App != "Test App" || body.Environment != "test" || body.ReceiveMode != "polling" {
		t.Fatalf("unexpected health response: %+v", body)
	}
}

func TestReady(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/ready", nil)
	recorder := httptest.NewRecorder()
	New(testConfig(t)).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if recorder.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", recorder.Header().Get("Content-Type"))
	}
}

func TestReadyChecksDatabase(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{name: "available", status: http.StatusOK},
		{name: "unavailable", err: errors.New("database offline"), status: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			handler := New(testConfig(t), Dependencies{
				ReadyCheck: func(context.Context) error {
					called = true
					return test.err
				},
			})
			request := httptest.NewRequest(http.MethodGet, "/api/ready", nil)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if !called || recorder.Code != test.status {
				t.Fatalf(
					"called/status = %v/%d, want true/%d",
					called,
					recorder.Code,
					test.status,
				)
			}
		})
	}
}

func TestServesIndexAndStaticFiles(t *testing.T) {
	handler := New(testConfig(t))
	tests := []struct {
		path string
		want string
	}{
		{path: "/", want: "<h1>test index</h1>"},
		{path: "/static/app.js", want: "console.log('test');"},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
			}
			body, err := io.ReadAll(recorder.Result().Body)
			if err != nil {
				t.Fatalf("read response: %v", err)
			}
			if string(body) != test.want {
				t.Fatalf("body = %q, want %q", body, test.want)
			}
		})
	}
}

func TestRejectsUnknownRouteAndWrongMethod(t *testing.T) {
	handler := New(testConfig(t))
	tests := []struct {
		method string
		path   string
		want   int
	}{
		{method: http.MethodGet, path: "/missing", want: http.StatusNotFound},
		{method: http.MethodPost, path: "/api/health", want: http.StatusMethodNotAllowed},
	}

	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != test.want {
			t.Fatalf("%s %s status = %d, want %d", test.method, test.path, recorder.Code, test.want)
		}
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"index.html": "<h1>test index</h1>",
		"app.js":     "console.log('test');",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return config.Config{
		AppName:                 "Test App",
		Environment:             "test",
		Host:                    "127.0.0.1",
		Port:                    8000,
		ReceiveMode:             "polling",
		StaticDir:               dir,
		ChainKey:                "bsc",
		SubscriptionTokenSymbol: "USDT",
		SubscriptionPrice:       "10",
		SubscriptionDays:        30,
	}
}
