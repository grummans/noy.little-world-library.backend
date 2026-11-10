package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"littleworld/internal/auth"
	"littleworld/internal/books"
	"littleworld/internal/dbtest"
	"littleworld/internal/storage"
	"littleworld/internal/storagetest"
)

func newTeacherServer(t *testing.T) (*Server, *storagetest.Mem) {
	t.Helper()
	srv, _, store := newTeacherServerWithDB(t)
	return srv, store
}

// loginCookie registers a teacher (once) and returns a valid session cookie.
func loginCookie(t *testing.T, srv *Server, email string, role string) *http.Cookie {
	t.Helper()
	h := srv.Router()

	rec := do(t, h, http.MethodPost, "/api/auth/register",
		fmt.Sprintf(`{"display_name":"Bé Test","email":%q,"password":"password123","role":%q}`, email, role))
	if rec.Code != http.StatusCreated {
		t.Fatalf("register %s: status %d: %s", email, rec.Code, rec.Body)
	}
	return sessionCookieFrom(t, do(t, h, http.MethodPost, "/api/auth/login",
		fmt.Sprintf(`{"email":%q,"password":"password123","role":%q}`, email, role)))
}

func multipartBody(t *testing.T, fields map[string]string, files map[string]struct {
	contentType string
	data        []byte
}) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	for field, file := range files {
		part, err := writer.CreatePart(map[string][]string{
			"Content-Disposition": {fmt.Sprintf(`form-data; name=%q; filename=%q`, field, field)},
			"Content-Type":        {file.contentType},
		})
		if err != nil {
			t.Fatalf("create part %s: %v", field, err)
		}
		if _, err := part.Write(file.data); err != nil {
			t.Fatalf("write part %s: %v", field, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	return body, writer.FormDataContentType()
}

func doUpload(t *testing.T, h http.Handler, path string, cookie *http.Cookie, body io.Reader, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(cookie)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func epubFixture(t *testing.T) []byte {
	t.Helper()
	return storagetest.EPUB(t)
}

func pngFixture(t *testing.T) []byte {
	t.Helper()
	return storagetest.PNG(t, 64)
}

func TestTeacherUploadPublishFlow(t *testing.T) {
	srv, store := newTeacherServer(t)
	h := srv.Router()
	cookie := loginCookie(t, srv, "teacher@example.com", auth.RoleTeacher)

	body, contentType := multipartBody(t,
		map[string]string{
			"title":       "Động vật quanh bé",
			"description": "Sách về các con vật gần gũi.",
			"access_type": books.AccessPaid,
			"price_vnd":   "99000",
			"age_min":     "3",
			"age_max":     "6",
		},
		map[string]struct {
			contentType string
			data        []byte
		}{
			"epub":  {storage.EpubContentType, epubFixture(t)},
			"cover": {"image/png", pngFixture(t)},
		})

	created := doUpload(t, h, "/api/teacher/books", cookie, body, contentType)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", created.Code, created.Body)
	}

	var book bookResponse
	if err := json.Unmarshal(created.Body.Bytes(), &book); err != nil {
		t.Fatalf("decode book: %v", err)
	}
	if book.Slug != "dong-vat-quanh-be" || book.Status != books.StatusDraft {
		t.Errorf("book = %+v", book)
	}
	if !book.HasEpub || !book.HasCover {
		t.Errorf("files not attached: %+v", book)
	}
	if book.PriceVND == nil || *book.PriceVND != 99000 {
		t.Errorf("price = %v", book.PriceVND)
	}
	for _, key := range store.Keys() {
		if strings.HasPrefix(key, "uploads/") {
			t.Errorf("staging object left behind: %s", key)
		}
	}

	list := do(t, h, http.MethodGet, "/api/teacher/books", "", cookie)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d", list.Code)
	}
	var listed []bookResponse
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed) != 1 {
		t.Errorf("list has %d books, want 1", len(listed))
	}

	published := do(t, h, http.MethodPost, fmt.Sprintf("/api/teacher/books/%d/publish", book.ID), "", cookie)
	if published.Code != http.StatusOK {
		t.Fatalf("publish status = %d: %s", published.Code, published.Body)
	}
	var publishedBook bookResponse
	if err := json.Unmarshal(published.Body.Bytes(), &publishedBook); err != nil {
		t.Fatalf("decode published: %v", err)
	}
	if publishedBook.Status != books.StatusPublished || publishedBook.PublishedAt == nil {
		t.Errorf("published = %+v", publishedBook)
	}
}

func TestTeacherUploadRejectsBrokenEPUB(t *testing.T) {
	srv, store := newTeacherServer(t)
	cookie := loginCookie(t, srv, "teacher@example.com", auth.RoleTeacher)

	body, contentType := multipartBody(t,
		map[string]string{"title": "Sách lỗi", "access_type": books.AccessFree},
		map[string]struct {
			contentType string
			data        []byte
		}{
			"epub": {storage.EpubContentType, []byte("not a real epub")},
		})

	rec := doUpload(t, srv.Router(), "/api/teacher/books", cookie, body, contentType)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "invalid_file") {
		t.Errorf("body = %s, want invalid_file code", rec.Body)
	}
	if keys := store.Keys(); len(keys) != 0 {
		t.Errorf("objects left after rejected upload: %v", keys)
	}
}

func TestTeacherRoutesRejectParentAndOtherTeacher(t *testing.T) {
	srv, _ := newTeacherServer(t)
	h := srv.Router()

	teacherCookie := loginCookie(t, srv, "teacher@example.com", auth.RoleTeacher)
	parentCookie := loginCookie(t, srv, "parent@example.com", auth.RoleParent)

	if rec := do(t, h, http.MethodGet, "/api/teacher/books", "", parentCookie); rec.Code != http.StatusForbidden {
		t.Errorf("parent status = %d, want 403", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/api/teacher/books", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous status = %d, want 401", rec.Code)
	}

	otherCookie := loginCookie(t, srv, "other@example.com", auth.RoleTeacher)

	body, contentType := multipartBody(t,
		map[string]string{"title": "Sách của giáo viên A", "access_type": books.AccessFree},
		map[string]struct {
			contentType string
			data        []byte
		}{})
	created := doUpload(t, h, "/api/teacher/books", teacherCookie, body, contentType)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", created.Code, created.Body)
	}
	var book bookResponse
	if err := json.Unmarshal(created.Body.Bytes(), &book); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if rec := do(t, h, http.MethodGet, fmt.Sprintf("/api/teacher/books/%d", book.ID), "", otherCookie); rec.Code != http.StatusNotFound {
		t.Errorf("other teacher read status = %d, want 404", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, fmt.Sprintf("/api/teacher/books/%d/publish", book.ID), "", otherCookie); rec.Code != http.StatusNotFound {
		t.Errorf("other teacher publish status = %d, want 404", rec.Code)
	}
}

func TestPublishWithoutFilesIsConflict(t *testing.T) {
	srv, _ := newTeacherServer(t)
	cookie := loginCookie(t, srv, "teacher@example.com", auth.RoleTeacher)

	body, contentType := multipartBody(t,
		map[string]string{"title": "Chưa có tệp", "access_type": books.AccessFree},
		map[string]struct {
			contentType string
			data        []byte
		}{})
	created := doUpload(t, srv.Router(), "/api/teacher/books", cookie, body, contentType)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", created.Code, created.Body)
	}
	var book bookResponse
	if err := json.Unmarshal(created.Body.Bytes(), &book); err != nil {
		t.Fatalf("decode: %v", err)
	}

	rec := do(t, srv.Router(), http.MethodPost, fmt.Sprintf("/api/teacher/books/%d/publish", book.ID), "", cookie)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "not_publishable" {
		t.Errorf("code = %q, want not_publishable", code)
	}
}

func TestUpdateBookMetadata(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/teacher/books/%d", book.ID),
		strings.NewReader(`{"title":"Sách đã sửa","description":"Mô tả","access_type":"free"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d: %s", rec.Code, rec.Body)
	}
	var updated bookResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode updated: %v", err)
	}
	// The slug is part of the public URL, so editing the title must not move the book.
	if updated.Slug != book.Slug || updated.Description != "Mô tả" {
		t.Errorf("updated = %+v", updated)
	}
}

func TestUploadsUnavailableWithoutStorage(t *testing.T) {
	pool := dbtest.Migrated(t)
	srv := New(Deps{
		DB:            pool,
		Auth:          auth.New(pool),
		Books:         books.New(pool, nil),
		Log:           slog.New(slog.DiscardHandler),
		SecureCookies: false,
	})
	cookie := loginCookie(t, srv, "teacher@example.com", auth.RoleTeacher)

	body, contentType := multipartBody(t,
		map[string]string{"title": "Không có storage", "access_type": books.AccessFree},
		map[string]struct {
			contentType string
			data        []byte
		}{
			"cover": {"image/png", pngFixture(t)},
		})

	rec := doUpload(t, srv.Router(), "/api/teacher/books", cookie, body, contentType)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body)
	}
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return payload.Code
}

func TestBundleFlow(t *testing.T) {
	srv, _ := newTeacherServer(t)
	h := srv.Router()
	cookie := loginCookie(t, srv, "teacher@example.com", auth.RoleTeacher)

	// Two paid books and one free book owned by the teacher.
	paidIDs := make([]int64, 0, 2)
	for _, title := range []string{"Sách trả phí một", "Sách trả phí hai"} {
		body, contentType := multipartBody(t,
			map[string]string{
				"title":       title,
				"description": "Mô tả",
				"access_type": books.AccessPaid,
				"price_vnd":   "99000",
			},
			map[string]struct {
				contentType string
				data        []byte
			}{
				"epub":  {storage.EpubContentType, epubFixture(t)},
				"cover": {"image/png", pngFixture(t)},
			})
		created := doUpload(t, h, "/api/teacher/books", cookie, body, contentType)
		if created.Code != http.StatusCreated {
			t.Fatalf("create book %q: %d: %s", title, created.Code, created.Body)
		}
		var book bookResponse
		if err := json.Unmarshal(created.Body.Bytes(), &book); err != nil {
			t.Fatalf("decode book: %v", err)
		}
		if published := do(t, h, http.MethodPost, "/api/teacher/books/"+itoa(book.ID)+"/publish", "", cookie); published.Code != http.StatusOK {
			t.Fatalf("publish book: %d: %s", published.Code, published.Body)
		}
		paidIDs = append(paidIDs, book.ID)
	}

	body, contentType := multipartBody(t,
		map[string]string{
			"title":       "Gói hai sách",
			"description": "Gói dành cho phụ huynh",
			"price_vnd":   "199000",
		},
		map[string]struct {
			contentType string
			data        []byte
		}{"cover": {"image/png", pngFixture(t)}})
	// book_ids repeated as multipart fields.
	body, contentType = multipartBody(t,
		map[string]string{
			"title":       "Gói hai sách",
			"description": "Gói dành cho phụ huynh",
			"price_vnd":   "199000",
			"book_ids":    itoa(paidIDs[0]),
		},
		map[string]struct {
			contentType string
			data        []byte
		}{"cover": {"image/png", pngFixture(t)}})

	created := doUpload(t, h, "/api/teacher/bundles", cookie, body, contentType)
	if created.Code != http.StatusCreated {
		t.Fatalf("create bundle status = %d: %s", created.Code, created.Body)
	}
	var bundle bundleResponse
	if err := json.Unmarshal(created.Body.Bytes(), &bundle); err != nil {
		t.Fatalf("decode bundle: %v", err)
	}
	if bundle.Slug != "goi-hai-sach" || !bundle.HasCover || len(bundle.BookIDs) != 1 {
		t.Errorf("bundle = %+v", bundle)
	}

	published := do(t, h, http.MethodPost, "/api/teacher/bundles/"+itoa(bundle.ID)+"/publish", "", cookie)
	if published.Code != http.StatusOK {
		t.Fatalf("publish bundle: %d: %s", published.Code, published.Body)
	}

	list := do(t, h, http.MethodGet, "/api/teacher/bundles", "", cookie)
	if list.Code != http.StatusOK {
		t.Fatalf("list bundles: %d", list.Code)
	}
	var bundles []bundleResponse
	if err := json.Unmarshal(list.Body.Bytes(), &bundles); err != nil {
		t.Fatalf("decode bundles: %v", err)
	}
	if len(bundles) != 1 {
		t.Errorf("bundles = %d, want 1", len(bundles))
	}
}

func TestBundleRejectsFreeBook(t *testing.T) {
	srv, _ := newTeacherServer(t)
	h := srv.Router()
	cookie := loginCookie(t, srv, "teacher@example.com", auth.RoleTeacher)

	body, contentType := multipartBody(t,
		map[string]string{"title": "Sách miễn phí", "access_type": books.AccessFree},
		map[string]struct {
			contentType string
			data        []byte
		}{})
	created := doUpload(t, h, "/api/teacher/books", cookie, body, contentType)
	var freeBook bookResponse
	if err := json.Unmarshal(created.Body.Bytes(), &freeBook); err != nil {
		t.Fatalf("decode: %v", err)
	}

	bundleBody, bundleType := multipartBody(t,
		map[string]string{
			"title":     "Gói lỗi",
			"price_vnd": "100000",
			"book_ids":  itoa(freeBook.ID),
		},
		map[string]struct {
			contentType string
			data        []byte
		}{})
	rec := doUpload(t, h, "/api/teacher/bundles", cookie, bundleBody, bundleType)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestTagsEndpoint(t *testing.T) {
	srv, _ := newTeacherServer(t)
	h := srv.Router()

	rec := do(t, h, http.MethodGet, "/api/tags", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("body = %s, want []", got)
	}
}

func TestCatalogEndpoints(t *testing.T) {
	srv, _ := newTeacherServer(t)
	h := srv.Router()
	cookie := loginCookie(t, srv, "teacher@example.com", auth.RoleTeacher)

	// Publish one book, leave another as draft.
	body, contentType := multipartBody(t,
		map[string]string{
			"title":       "Động vật quanh bé",
			"description": "Sách về động vật",
			"access_type": books.AccessFree,
		},
		map[string]struct {
			contentType string
			data        []byte
		}{
			"epub":  {storage.EpubContentType, epubFixture(t)},
			"cover": {"image/png", pngFixture(t)},
		})
	created := doUpload(t, h, "/api/teacher/books", cookie, body, contentType)
	if created.Code != http.StatusCreated {
		t.Fatalf("create book: %d: %s", created.Code, created.Body)
	}
	var book bookResponse
	if err := json.Unmarshal(created.Body.Bytes(), &book); err != nil {
		t.Fatalf("decode book: %v", err)
	}
	if rec := do(t, h, http.MethodPost, "/api/teacher/books/"+itoa(book.ID)+"/publish", "", cookie); rec.Code != http.StatusOK {
		t.Fatalf("publish: %d: %s", rec.Code, rec.Body)
	}

	draftBody, draftType := multipartBody(t,
		map[string]string{"title": "Sách nháp", "access_type": books.AccessFree},
		map[string]struct {
			contentType string
			data        []byte
		}{})
	if rec := doUpload(t, h, "/api/teacher/books", cookie, draftBody, draftType); rec.Code != http.StatusCreated {
		t.Fatalf("create draft: %d", rec.Code)
	}

	// Public list only shows the published book.
	list := do(t, h, http.MethodGet, "/api/books", "")
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", list.Code, list.Body)
	}
	var payload struct {
		Items   []cardResponse `json:"items"`
		HasMore bool           `json:"has_more"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].Slug != "dong-vat-quanh-be" {
		t.Fatalf("items = %+v", payload.Items)
	}
	if payload.HasMore {
		t.Error("has_more = true on a single item")
	}
	if payload.Items[0].CoverURL == "" {
		t.Error("cover url missing in the public list")
	}

	// Detail by slug; the draft slug is not reachable.
	if rec := do(t, h, http.MethodGet, "/api/books/"+book.Slug, ""); rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d: %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, http.MethodGet, "/api/books/sach-nhap", ""); rec.Code != http.StatusNotFound {
		t.Errorf("draft detail status = %d, want 404", rec.Code)
	}

	// Bad kind is a validation error, not a 500.
	if rec := do(t, h, http.MethodGet, "/api/books?kind=nope", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad kind status = %d, want 400", rec.Code)
	}
}

func TestReadEndpointAccess(t *testing.T) {
	srv, _ := newTeacherServer(t)
	h := srv.Router()
	teacher := loginCookie(t, srv, "teacher@example.com", auth.RoleTeacher)

	// Publish a free book and a paid book.
	mkBook := func(title, access, price string) bookResponse {
		fields := map[string]string{"title": title, "description": "Mô tả", "access_type": access}
		if price != "" {
			fields["price_vnd"] = price
		}
		body, contentType := multipartBody(t, fields, map[string]struct {
			contentType string
			data        []byte
		}{
			"epub":  {storage.EpubContentType, epubFixture(t)},
			"cover": {"image/png", pngFixture(t)},
		})
		created := doUpload(t, h, "/api/teacher/books", teacher, body, contentType)
		if created.Code != http.StatusCreated {
			t.Fatalf("create %s: %d: %s", title, created.Code, created.Body)
		}
		var book bookResponse
		if err := json.Unmarshal(created.Body.Bytes(), &book); err != nil {
			t.Fatalf("decode book: %v", err)
		}
		if rec := do(t, h, http.MethodPost, "/api/teacher/books/"+itoa(book.ID)+"/publish", "", teacher); rec.Code != http.StatusOK {
			t.Fatalf("publish %s: %d: %s", title, rec.Code, rec.Body)
		}
		return book
	}

	free := mkBook("Sách miễn phí", books.AccessFree, "")
	paid := mkBook("Sách trả phí", books.AccessPaid, "99000")

	// Free book: anonymous access returns a signed URL and never an object key.
	anon := do(t, h, http.MethodGet, "/api/books/"+free.Slug+"/read", "")
	if anon.Code != http.StatusOK {
		t.Fatalf("free read status = %d: %s", anon.Code, anon.Body)
	}
	var session struct {
		URL       string `json:"url"`
		Source    string `json:"source"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(anon.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if session.URL == "" || session.Source != "free" || session.ExpiresIn <= 0 {
		t.Errorf("session = %+v", session)
	}
	// The response carries a URL, never the stored key field.
	if strings.Contains(anon.Body.String(), "epub_object_key") {
		t.Error("response exposed the object key field")
	}

	// Paid book: anonymous and unrelated users are refused.
	if rec := do(t, h, http.MethodGet, "/api/books/"+paid.Slug+"/read", ""); rec.Code != http.StatusForbidden {
		t.Errorf("anonymous paid read status = %d, want 403", rec.Code)
	}
	parent := loginCookie(t, srv, "parent@example.com", auth.RoleParent)
	if rec := do(t, h, http.MethodGet, "/api/books/"+paid.Slug+"/read", "", parent); rec.Code != http.StatusForbidden {
		t.Errorf("parent without order status = %d, want 403", rec.Code)
	}
}

func TestReadEndpointHidesDrafts(t *testing.T) {
	srv, _ := newTeacherServer(t)
	h := srv.Router()
	teacher := loginCookie(t, srv, "teacher@example.com", auth.RoleTeacher)

	body, contentType := multipartBody(t,
		map[string]string{"title": "Sách nháp", "access_type": books.AccessFree},
		map[string]struct {
			contentType string
			data        []byte
		}{})
	if rec := doUpload(t, h, "/api/teacher/books", teacher, body, contentType); rec.Code != http.StatusCreated {
		t.Fatalf("create draft: %d", rec.Code)
	}

	if rec := do(t, h, http.MethodGet, "/api/books/sach-nhap/read", ""); rec.Code != http.StatusNotFound {
		t.Errorf("draft read status = %d, want 404", rec.Code)
	}
}
