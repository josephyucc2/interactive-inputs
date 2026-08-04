package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/boasihq/interactive-inputs/internal/session"
	"github.com/gorilla/mux"
)

func TestHealth(t *testing.T) {
	content := fstest.MapFS{
		"internal/web/ui/static/placeholder.txt": &fstest.MapFile{Data: []byte("ok")},
	}
	server := New(session.NewManager(), content, "internal/", &Config{Port: 9090, SessionTimeout: 300})
	router := mux.NewRouter()
	server.AttachRoutes(router)

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("expected health status ok, got %q", body["status"])
	}
}
