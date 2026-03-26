package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/boasihq/interactive-inputs/internal/fields"
	"github.com/boasihq/interactive-inputs/internal/session"
	"github.com/boasihq/interactive-inputs/internal/toolbox"
	webui "github.com/boasihq/interactive-inputs/internal/web"
	"github.com/gorilla/mux"
)

// Config holds the server-mode configuration
type Config struct {
	Port           int
	SessionTimeout int // default timeout for sessions in seconds
}

// Server is the persistent server-mode handler
type Server struct {
	sessionManager                *session.Manager
	embeddedContent               fs.FS
	embeddedContentFilePathPrefix string
	config                        *Config
}

// New creates a new Server instance
func New(sm *session.Manager, embeddedContent fs.FS, prefix string, cfg *Config) *Server {
	return &Server{
		sessionManager:                sm,
		embeddedContent:               embeddedContent,
		embeddedContentFilePathPrefix: prefix,
		config:                        cfg,
	}
}

// AttachRoutes registers all server-mode routes on the router
func (s *Server) AttachRoutes(router *mux.Router) {
	// Static assets (shared with action mode)
	staticSubFS, err := fs.Sub(s.embeddedContent, fmt.Sprintf("%sweb/ui/static", s.embeddedContentFilePathPrefix))
	if err != nil {
		log.Fatalf("unable to create static asset filesystem: %v", err)
	}
	router.PathPrefix("/static/").Handler(http.StripPrefix("/static/", http.FileServer(http.FS(staticSubFS))))

	// REST API
	api := router.PathPrefix("/api/sessions").Subrouter()
	api.HandleFunc("", s.CreateSession).Methods("POST")
	api.HandleFunc("/{id}", s.GetSession).Methods("GET")
	api.HandleFunc("/{id}/wait", s.WaitSession).Methods("GET")

	// Portal UI (user-facing form pages)
	router.HandleFunc("/portal/{id}", s.PortalPage).Methods("GET")
	router.HandleFunc("/portal/{id}/submit", s.PortalSubmit).Methods("POST")
	router.HandleFunc("/portal/{id}/cancel", s.PortalCancel).Methods("POST")

	// File upload/reset endpoints scoped to session
	router.HandleFunc("/portal/{id}/api/v1/upload", s.PortalUpload).Methods("POST", "OPTIONS")
	router.HandleFunc("/portal/{id}/api/v1/reset/{inputFieldVariableId}", s.PortalResetUpload).Methods("DELETE", "OPTIONS")
}

// --- API Handlers ---

// CreateSessionRequest is the JSON body for POST /api/sessions
type CreateSessionRequest struct {
	Title   string         `json:"title"`
	Timeout int            `json:"timeout"`
	Fields  *fields.Fields `json:"fields"`
}

// CreateSessionResponse is the JSON response for POST /api/sessions
type CreateSessionResponse struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	PortalURL string    `json:"portal_url"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SessionResponse is the JSON response for session queries
type SessionResponse struct {
	ID        string            `json:"id"`
	Status    string            `json:"status"`
	Results   map[string]string `json:"results,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt time.Time         `json:"expires_at"`
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// CreateSession handles POST /api/sessions
func (s *Server) CreateSession(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, fmt.Sprintf("invalid request body: %s", err), http.StatusBadRequest)
		return
	}

	if req.Fields == nil || len(req.Fields.Fields) == 0 {
		jsonError(w, "fields are required", http.StatusBadRequest)
		return
	}

	if req.Timeout <= 0 {
		req.Timeout = s.config.SessionTimeout
	}

	// Validate and normalize fields
	for i, field := range req.Fields.Fields {
		validType := false
		for _, vt := range fields.ValidFieldTypes {
			if strings.EqualFold(field.Properties.Type, vt) {
				validType = true
				break
			}
		}
		if !validType {
			jsonError(w, fmt.Sprintf("invalid field type: %s", field.Properties.Type), http.StatusBadRequest)
			return
		}
		req.Fields.Fields[i].Properties.Type = strings.ToLower(field.Properties.Type)

		// Convert label to kebab-case
		labelKebab, err := toolbox.StringConvertToKebabCase(
			toolbox.StringRemoveSpecialCharactersWith(field.Label, ""),
		)
		if err != nil || labelKebab == "" {
			jsonError(w, fmt.Sprintf("invalid label: %s", field.Label), http.StatusBadRequest)
			return
		}
		req.Fields.Fields[i].Label = labelKebab

		// Resolve choicesFilePath eagerly
		if field.Properties.ChoicesFilePath != "" && len(field.Properties.Choices) == 0 {
			choices, err := fields.LoadChoicesFromFile(field.Properties.ChoicesFilePath)
			if err != nil {
				jsonError(w, fmt.Sprintf("failed to load choices from %s: %s", field.Properties.ChoicesFilePath, err), http.StatusBadRequest)
				return
			}
			req.Fields.Fields[i].Properties.Choices = choices
		}
	}

	sess, err := s.sessionManager.CreateSession(req.Title, req.Fields, req.Timeout)
	if err != nil {
		jsonError(w, fmt.Sprintf("failed to create session: %s", err), http.StatusInternalServerError)
		return
	}

	resp := CreateSessionResponse{
		ID:        sess.ID,
		Status:    string(sess.Status),
		PortalURL: fmt.Sprintf("/portal/%s", sess.ID),
		CreatedAt: sess.CreatedAt,
		ExpiresAt: sess.ExpiresAt,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

// GetSession handles GET /api/sessions/{id}
func (s *Server) GetSession(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	sess, ok := s.sessionManager.GetSession(id)
	if !ok {
		jsonError(w, "session not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SessionResponse{
		ID:        sess.ID,
		Status:    string(sess.Status),
		Results:   sess.Results,
		CreatedAt: sess.CreatedAt,
		ExpiresAt: sess.ExpiresAt,
	})
}

// WaitSession handles GET /api/sessions/{id}/wait (long-poll)
func (s *Server) WaitSession(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	timeout := 300
	if t := r.URL.Query().Get("timeout"); t != "" {
		if parsed, err := strconv.Atoi(t); err == nil && parsed > 0 {
			timeout = parsed
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeout)*time.Second)
	defer cancel()

	sess, err := s.sessionManager.WaitForSession(ctx, id)
	if err != nil {
		if err == context.DeadlineExceeded {
			jsonError(w, "wait timeout", http.StatusRequestTimeout)
			return
		}
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SessionResponse{
		ID:        sess.ID,
		Status:    string(sess.Status),
		Results:   sess.Results,
		CreatedAt: sess.CreatedAt,
		ExpiresAt: sess.ExpiresAt,
	})
}

// --- Portal Handlers (user-facing web UI) ---

// PortalPage renders the form page for a session
func (s *Server) PortalPage(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	sess, ok := s.sessionManager.GetSession(id)
	if !ok {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	if sess.Status != session.StatusPending {
		http.Error(w, fmt.Sprintf("Session is %s", sess.Status), http.StatusGone)
		return
	}

	basePath := fmt.Sprintf("/portal/%s", id)
	remainingSec := int(time.Until(sess.ExpiresAt).Seconds())
	if remainingSec < 0 {
		remainingSec = 0
	}

	response := &webui.CreateInteractiveInputsPortalRequest{
		RepoOwner: "Interactive Inputs",
		Title:     sess.Title,
		Fields:    sess.Fields,
		Timeout:   toolbox.SecondsToMinutes(remainingSec),
		BasePath:  basePath,
	}

	templateFilesToParse := []string{
		fmt.Sprintf("%sweb/ui/html/index.tmpl.html", s.embeddedContentFilePathPrefix),
		fmt.Sprintf("%sweb/ui/html/partials/shared/head-meta.tmpl.html", s.embeddedContentFilePathPrefix),
		fmt.Sprintf("%sweb/ui/html/pages/@landing.tmpl.html", s.embeddedContentFilePathPrefix),
		fmt.Sprintf("%sweb/ui/html/partials/shared/tailwind-dash-script.tmpl.html", s.embeddedContentFilePathPrefix),
	}

	parsedTemplates, err := template.ParseFS(s.embeddedContent, templateFilesToParse...)
	if err != nil {
		log.Printf("template parse error: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if err := parsedTemplates.ExecuteTemplate(w, "base", response); err != nil {
		log.Printf("template execute error: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// PortalSubmit handles form submission for a session
func (s *Server) PortalSubmit(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	sess, ok := s.sessionManager.GetSession(id)
	if !ok {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	r.ParseForm()
	results := make(map[string]string)

	for key, value := range r.Form {
		if cacheDir, exists := sess.InputFieldLabelToCacheDirMapping[key]; exists && cacheDir != "" {
			results[key] = cacheDir
		} else {
			results[key] = strings.Join(value, ",")
		}
	}

	if err := s.sessionManager.SubmitSession(id, results); err != nil {
		http.Error(w, fmt.Sprintf("Failed to submit: %s", err), http.StatusBadRequest)
		return
	}

	s.renderResponseTemplate(w, "success.tmpl.html")
}

// PortalCancel handles form cancellation for a session
func (s *Server) PortalCancel(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	if err := s.sessionManager.CancelSession(id); err != nil {
		http.Error(w, fmt.Sprintf("Failed to cancel: %s", err), http.StatusBadRequest)
		return
	}

	s.renderResponseTemplate(w, "cancel.tmpl.html")
}

func (s *Server) renderResponseTemplate(w http.ResponseWriter, tmplName string) {
	tmplPath := fmt.Sprintf("%sweb/ui/html/partials/responses/%s", s.embeddedContentFilePathPrefix, tmplName)
	parsedTemplates, err := template.ParseFS(s.embeddedContent, tmplPath)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Trigger", "template-executed")
	w.Header().Set("HX-Trigger-After-Swap", "template-swapped")
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)

	parsedTemplates.Execute(w, map[string]string{"JobUrl": ""})
}

// PortalUpload handles file uploads for a session
func (s *Server) PortalUpload(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	sess, ok := s.sessionManager.GetSession(id)
	if !ok {
		jsonError(w, "session not found", http.StatusNotFound)
		return
	}

	const indexKeySplitter string = "__index__"
	var successFiles []string
	var failedFiles []string

	r.ParseMultipartForm(10 << 20)
	if r.MultipartForm == nil {
		jsonError(w, "no files provided", http.StatusBadRequest)
		return
	}

	files := r.MultipartForm.File
	totalFiles := len(files)

	for k := range files {
		file, handler, err := r.FormFile(k)
		if err != nil {
			failedFiles = append(failedFiles, k)
			continue
		}
		defer file.Close()

		indexArray := strings.Split(k, indexKeySplitter)
		inputFieldLabel := indexArray[0]

		fileBytes, err := io.ReadAll(file)
		if err != nil {
			failedFiles = append(failedFiles, handler.Filename)
			continue
		}

		inputCacheDir := sess.InputFieldLabelToCacheDirMapping[inputFieldLabel]
		if inputCacheDir == "" {
			failedFiles = append(failedFiles, handler.Filename)
			continue
		}

		err = os.WriteFile(fmt.Sprintf("%s/%s", inputCacheDir, handler.Filename), fileBytes, 0644)
		if err != nil {
			failedFiles = append(failedFiles, handler.Filename)
			continue
		}

		successFiles = append(successFiles, handler.Filename)
	}

	status := "success"
	if len(failedFiles) > 0 && len(successFiles) > 0 {
		status = "partial success"
	}
	if len(successFiles) == 0 && len(failedFiles) > 0 {
		status = "failed"
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":         status,
		"uploaded_files": successFiles,
		"failed_files":   failedFiles,
		"total_files":    totalFiles,
	})
}

// PortalResetUpload handles resetting uploaded files for a session
func (s *Server) PortalResetUpload(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	inputFieldLabel := mux.Vars(r)["inputFieldVariableId"]

	sess, ok := s.sessionManager.GetSession(id)
	if !ok {
		jsonError(w, "session not found", http.StatusNotFound)
		return
	}

	cacheDir := sess.InputFieldLabelToCacheDirMapping[inputFieldLabel]
	if cacheDir == "" {
		jsonError(w, "no cache directory found for field", http.StatusBadRequest)
		return
	}

	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		jsonError(w, "unable to read cache directory", http.StatusInternalServerError)
		return
	}

	var deletedFiles []string
	for _, entry := range entries {
		fullPath := fmt.Sprintf("%s/%s", cacheDir, entry.Name())
		if err := os.RemoveAll(fullPath); err == nil {
			deletedFiles = append(deletedFiles, entry.Name())
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":              "success",
		"deleted_files":       deletedFiles,
		"total_files_deleted": len(deletedFiles),
	})
}
