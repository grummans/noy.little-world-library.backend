package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"littleworld/internal/auth"
	"littleworld/internal/books"
	"littleworld/internal/bundles"
	"littleworld/internal/catalog"
	"littleworld/internal/dbtest"
	"littleworld/internal/storage"
	"littleworld/internal/storagetest"
)

func TestLoginIsRateLimited(t *testing.T) {
	srv, _ := newTeacherServer(t)
	h := srv.Router()

	attempt := func() int {
		return do(t, h, http.MethodPost, "/api/auth/login",
			`{"email":"nobody@example.com","password":"password123"}`).Code
	}

	for i := 0; i < 10; i++ {
		if code := attempt(); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, code)
		}
	}
	if code := attempt(); code != http.StatusTooManyRequests {
		t.Fatalf("attempt 11: status = %d, want 429", code)
	}
}

func TestCrossSitePostIsRejected(t *testing.T) {
	srv, _ := newTeacherServer(t)
	h := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"email":"a@example.com","password":"password123"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestSameOriginPostIsAllowed(t *testing.T) {
	srv, _ := newTeacherServer(t)
	h := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/api/auth/register",
		strings.NewReader(`{"display_name":"Bé Test","email":"same@example.com","password":"password123","role":"parent"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
}

func TestJSONBodyNeedsJSONContentType(t *testing.T) {
	srv, _ := newTeacherServer(t)
	h := srv.Router()
	cookie := loginCookie(t, srv, "teacher@example.com", auth.RoleTeacher)

	body, contentType := multipartBody(t,
		map[string]string{"title": "Sách nháp", "access_type": books.AccessFree},
		map[string]struct {
			contentType string
			data        []byte
		}{})
	created := doUpload(t, h, "/api/teacher/books", cookie, body, contentType)
	var book bookResponse
	if err := json.Unmarshal(created.Body.Bytes(), &book); err != nil {
		t.Fatalf("decode: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/teacher/books/"+itoa(book.ID),
		strings.NewReader(`{"title":"X","access_type":"free"}`))
	req.Header.Set("Content-Type", "text/plain")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

// Keep the helpers honest: both fixtures must be accepted by the validators.
func TestFixturesPassValidation(t *testing.T) {
	epub := storagetest.EPUB(t)
	if err := storage.ValidateEPUB(storage.EpubContentType, int64(len(epub))); err != nil {
		t.Fatalf("epub fixture rejected: %v", err)
	}

	cover := storagetest.PNG(t, 64)
	if err := storage.ValidateCover("image/png", int64(len(cover))); err != nil {
		t.Fatalf("cover fixture rejected: %v", err)
	}
}

func TestTeacherCreatesAndDeletesTags(t *testing.T) {
	srv, _ := newTeacherServer(t)
	h := srv.Router()
	teacher := loginCookie(t, srv, "teacher@example.com", auth.RoleTeacher)
	parent := loginCookie(t, srv, "parent@example.com", auth.RoleParent)

	if rec := do(t, h, http.MethodPost, "/api/teacher/tags", `{"name":"Động vật"}`, parent); rec.Code != http.StatusForbidden {
		t.Errorf("parent create tag status = %d, want 403", rec.Code)
	}

	created := do(t, h, http.MethodPost, "/api/teacher/tags", `{"name":"Động vật"}`, teacher)
	if created.Code != http.StatusCreated {
		t.Fatalf("create tag status = %d: %s", created.Code, created.Body)
	}
	var createdTag tagResponse
	if err := json.Unmarshal(created.Body.Bytes(), &createdTag); err != nil {
		t.Fatalf("decode tag: %v", err)
	}
	if createdTag.Slug != "dong-vat" {
		t.Errorf("slug = %q", createdTag.Slug)
	}

	if rec := do(t, h, http.MethodPost, "/api/teacher/tags", `{"name":"dong vat"}`, teacher); rec.Code != http.StatusConflict {
		t.Errorf("duplicate tag status = %d, want 409", rec.Code)
	}

	list := do(t, h, http.MethodGet, "/api/tags", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "dong-vat") {
		t.Errorf("public list = %d %s", list.Code, list.Body)
	}

	if rec := do(t, h, http.MethodDelete, "/api/teacher/tags/"+itoa(createdTag.ID), "", teacher); rec.Code != http.StatusNoContent {
		t.Errorf("delete tag status = %d, want 204: %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, http.MethodDelete, "/api/teacher/tags/"+itoa(createdTag.ID), "", teacher); rec.Code != http.StatusNotFound {
		t.Errorf("delete twice status = %d, want 404", rec.Code)
	}

	// Another teacher may not delete a topic they did not create.
	other := loginCookie(t, srv, "other@example.com", auth.RoleTeacher)
	theirs := do(t, h, http.MethodPost, "/api/teacher/tags", `{"name":"Gia đình"}`, teacher)
	var theirTag tagResponse
	if err := json.Unmarshal(theirs.Body.Bytes(), &theirTag); err != nil {
		t.Fatalf("decode tag: %v", err)
	}
	if rec := do(t, h, http.MethodDelete, "/api/teacher/tags/"+itoa(theirTag.ID), "", other); rec.Code != http.StatusNotFound {
		t.Errorf("other teacher delete status = %d, want 404", rec.Code)
	}

	// A tag assigned to a book cannot be deleted.
	body, contentType := multipartBody(t,
		map[string]string{"title": "Sách có chủ đề", "access_type": books.AccessFree, "tag_id": itoa(theirTag.ID)},
		map[string]struct {
			contentType string
			data        []byte
		}{})
	if rec := doUpload(t, h, "/api/teacher/books", teacher, body, contentType); rec.Code != http.StatusCreated {
		t.Fatalf("create book with tag: %d: %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, http.MethodDelete, "/api/teacher/tags/"+itoa(theirTag.ID), "", teacher); rec.Code != http.StatusConflict {
		t.Errorf("delete assigned tag status = %d, want 409: %s", rec.Code, rec.Body)
	}
}

// failingPresigner simulates MinIO being unreachable while the API is healthy.
type failingPresigner struct{}

func (failingPresigner) PresignGet(context.Context, string) (string, error) {
	return "", errors.New("minio down")
}

func TestCatalogSurvivesPresignFailure(t *testing.T) {
	pool := dbtest.Migrated(t)
	db := pool
	srv := New(Deps{
		DB:            db,
		Auth:          auth.New(db),
		Books:         books.New(db, nil),
		Bundles:       bundles.New(db, nil),
		Catalog:       catalog.New(db, failingPresigner{}),
		Log:           slog.New(slog.DiscardHandler),
		SecureCookies: false,
	})

	body, contentType := multipartBody(t,
		map[string]string{"title": "Sách công khai", "access_type": books.AccessFree},
		map[string]struct {
			contentType string
			data        []byte
		}{})
	_ = body
	_ = contentType

	// Insert a published book directly: this test is about the read path only.
	var teacher int64
	if err := db.QueryRowContext(t.Context(),
		`INSERT INTO users (display_name, email, password_hash, role) VALUES ('Test User', 'pub@example.com', 'x', 'teacher') RETURNING id`,
	).Scan(&teacher); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.ExecContext(t.Context(),
		`INSERT INTO books (owner_id, slug, title, description, cover_object_key, epub_object_key,
			access_type, status, published_at)
		 VALUES ($1, 'sach', 'Sách', 'Mô tả', 'books/1/cover.png', 'books/1/book.epub', 'free', 'published', now())`,
		teacher); err != nil {
		t.Fatalf("insert book: %v", err)
	}

	rec := do(t, srv.Router(), http.MethodGet, "/api/books", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when presigning fails: %s", rec.Code, rec.Body)
	}
	var payload struct {
		Items []cardResponse `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].CoverURL != "" {
		t.Errorf("items = %+v, want one item with an empty cover_url", payload.Items)
	}
}

func TestCatalogHidesUnpublishedBundle(t *testing.T) {
	srv, _ := newTeacherServer(t)
	h := srv.Router()
	cookie := loginCookie(t, srv, "teacher@example.com", auth.RoleTeacher)

	body, contentType := multipartBody(t,
		map[string]string{"title": "Gói nháp", "price_vnd": "50000"},
		map[string]struct {
			contentType string
			data        []byte
		}{})
	if rec := doUpload(t, h, "/api/teacher/bundles", cookie, body, contentType); rec.Code != http.StatusCreated {
		t.Fatalf("create bundle: %d: %s", rec.Code, rec.Body)
	}

	if rec := do(t, h, http.MethodGet, "/api/bundles/goi-nhap", ""); rec.Code != http.StatusNotFound {
		t.Errorf("draft bundle detail status = %d, want 404", rec.Code)
	}
	rec := do(t, h, http.MethodGet, "/api/books?kind=bundle", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	var payload struct {
		Items []cardResponse `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Items) != 0 {
		t.Errorf("draft bundle leaked into the public list: %+v", payload.Items)
	}
}
