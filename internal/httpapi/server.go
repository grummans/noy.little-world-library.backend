// Package httpapi holds HTTP routing, handlers and middleware.
package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"littleworld/internal/activities"
	"littleworld/internal/auth"
	"littleworld/internal/books"
	"littleworld/internal/bundles"
	"littleworld/internal/catalog"
	"littleworld/internal/library"
	"littleworld/internal/orders"
	"littleworld/internal/reading"
	"littleworld/internal/sales"
	"littleworld/internal/storage"
)

type Deps struct {
	DB      *sql.DB
	Auth    *auth.Service
	Books   *books.Service
	Bundles *bundles.Service

	Activities *activities.Service
	Catalog    *catalog.Service
	Reading    *reading.Service
	Library    *library.Service
	Orders     *orders.Service
	Sales      *sales.Service
	Log        *slog.Logger
	// Storage is nil when S3 settings are absent; upload endpoints must check.
	Storage *storage.Client
	// SecureCookies must be true anywhere the site is served over HTTPS.
	SecureCookies bool
	// VnpayReturnURL is where VNPay sends the browser back to.
	VnpayReturnURL string
}

type Server struct {
	Deps

	limiter authLimiter
}

func New(deps Deps) *Server {
	return &Server{Deps: deps}
}

func (s *Server) Router() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)

	mux.HandleFunc("POST /api/auth/register", s.limit(s.handleRegister))
	mux.HandleFunc("POST /api/auth/login", s.limit(s.handleLogin))
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/auth/me", s.requireAuth(s.handleMe))

	mux.HandleFunc("GET /api/teacher/books", s.requireRole(auth.RoleTeacher, s.handleListBooks))
	mux.HandleFunc("POST /api/teacher/books", s.requireRole(auth.RoleTeacher, s.handleCreateBook))
	mux.HandleFunc("GET /api/teacher/books/{id}", s.requireRole(auth.RoleTeacher, s.handleGetBook))
	mux.HandleFunc("PUT /api/teacher/books/{id}", s.requireRole(auth.RoleTeacher, s.handleUpdateBook))
	mux.HandleFunc("PUT /api/teacher/books/{id}/files", s.requireRole(auth.RoleTeacher, s.handleReplaceBookFiles))
	mux.HandleFunc("POST /api/teacher/books/{id}/publish", s.requireRole(auth.RoleTeacher, s.handlePublishBook))
	mux.HandleFunc("POST /api/teacher/books/{id}/unpublish", s.requireRole(auth.RoleTeacher, s.handleUnpublishBook))

	mux.HandleFunc("GET /api/teacher/bundles", s.requireRole(auth.RoleTeacher, s.handleListBundles))
	mux.HandleFunc("POST /api/teacher/bundles", s.requireRole(auth.RoleTeacher, s.handleCreateBundle))
	mux.HandleFunc("GET /api/teacher/bundles/{id}", s.requireRole(auth.RoleTeacher, s.handleGetBundle))
	mux.HandleFunc("PUT /api/teacher/bundles/{id}", s.requireRole(auth.RoleTeacher, s.handleUpdateBundle))
	mux.HandleFunc("PUT /api/teacher/bundles/{id}/cover", s.requireRole(auth.RoleTeacher, s.handleReplaceBundleCover))
	mux.HandleFunc("POST /api/teacher/bundles/{id}/publish", s.requireRole(auth.RoleTeacher, s.handlePublishBundle))
	mux.HandleFunc("POST /api/teacher/bundles/{id}/unpublish", s.requireRole(auth.RoleTeacher, s.handleUnpublishBundle))

	mux.HandleFunc("GET /api/tags", s.handleListTags)

	mux.HandleFunc("GET /api/books", s.handleCatalogList)
	mux.HandleFunc("GET /api/books/{slug}", s.handleCatalogBook)
	mux.HandleFunc("GET /api/bundles/{slug}", s.handleCatalogBundle)
	mux.HandleFunc("GET /api/books/{slug}/read", s.withOptionalUser(s.handleReadBook))

	mux.HandleFunc("GET /api/parent/library", s.requireRole(auth.RoleParent, s.handleParentLibrary))

	mux.HandleFunc("GET /api/orders", s.requireRole(auth.RoleParent, s.handleListOrders))
	mux.HandleFunc("POST /api/orders", s.requireRole(auth.RoleParent, s.handleCreateOrder))
	mux.HandleFunc("GET /api/orders/{txnRef}", s.requireRole(auth.RoleParent, s.handleGetOrder))
	mux.HandleFunc("GET /api/payments/vnpay/return", s.handleVnpayReturn)
	mux.HandleFunc("GET /api/payments/vnpay/ipn", s.handleVnpayIPN)

	mux.HandleFunc("POST /api/teacher/tags", s.requireRole(auth.RoleTeacher, s.handleCreateTag))
	mux.HandleFunc("DELETE /api/teacher/tags/{id}", s.requireRole(auth.RoleTeacher, s.handleDeleteTag))
	mux.HandleFunc("GET /api/teacher/sales", s.requireRole(auth.RoleTeacher, s.handleSales))
	mux.HandleFunc("GET /api/activities", s.handlePublicActivities)
	mux.HandleFunc("GET /api/activities/facets", s.handleActivityFacets)
	mux.HandleFunc("GET /api/activities/{slug}", s.handlePublicActivity)
	mux.HandleFunc("GET /api/teacher/activities", s.requireRole(auth.RoleTeacher, s.handleListActivities))
	mux.HandleFunc("POST /api/teacher/activities", s.requireRole(auth.RoleTeacher, s.handleCreateActivity))
	mux.HandleFunc("GET /api/teacher/activities/{id}", s.requireRole(auth.RoleTeacher, s.handleGetActivity))
	mux.HandleFunc("PUT /api/teacher/activities/{id}", s.requireRole(auth.RoleTeacher, s.handleUpdateActivity))
	mux.HandleFunc("PUT /api/teacher/activities/{id}/thumbnail", s.requireRole(auth.RoleTeacher, s.handleReplaceActivityThumbnail))
	mux.HandleFunc("PUT /api/teacher/activities/{id}/status", s.requireRole(auth.RoleTeacher, s.handleSetActivityStatus))

	return s.checkOrigin(mux)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.DB.PingContext(ctx); err != nil {
		s.Log.Error("health check failed", "component", "database", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	if s.Storage != nil {
		if err := s.Storage.Check(ctx); err != nil {
			s.Log.Error("health check failed", "component", "storage", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("write json response", "err", err)
	}
}

// writeError keeps the wire format stable: a machine code plus a human message.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if contentType := r.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		return errors.New("content type must be application/json")
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}

	// Reject a second JSON value so proxies and the app cannot disagree on the body.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("body must contain a single json value")
	}
	return nil
}
