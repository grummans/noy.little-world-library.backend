package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func pngImage(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 30, G: 110, B: 180, A: 255})

	buf := &bytes.Buffer{}
	if err := png.Encode(buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func decodeActivity(t *testing.T, body []byte) activityResponse {
	t.Helper()

	var resp activityResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode activity: %v (%s)", err, body)
	}
	return resp
}

const activityJSON = `{"title":"Chủ đề động vật","description":"Bé nhận biết con vật.","embed_url":"https://wordwall.net/embed/629ec0e8ad7649f5b530f95fd81df87e?themeId=1","game_type":"Ghép đôi","topic":"Động vật","skills":["Nhận biết"],"age_min":3,"age_max":5}`

func TestActivityTeacherFlow(t *testing.T) {
	srv, _ := newTeacherServer(t)
	h := srv.Router()
	teacher := loginCookie(t, srv, "games@example.com", "teacher")

	created := do(t, h, http.MethodPost, "/api/teacher/activities", activityJSON, teacher)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", created.Code, created.Body)
	}
	activity := decodeActivity(t, created.Body.Bytes())
	if activity.Status != "draft" || activity.Slug != "chu-de-dong-vat" {
		t.Fatalf("created = %+v", activity)
	}

	// A draft is invisible to the public and to another teacher.
	public := do(t, h, http.MethodGet, "/api/activities/"+activity.Slug, "")
	if public.Code != http.StatusNotFound {
		t.Errorf("draft public status = %d, want 404", public.Code)
	}
	stranger := loginCookie(t, srv, "stranger@example.com", "teacher")
	if got := do(t, h, http.MethodGet, fmt.Sprintf("/api/teacher/activities/%d", activity.ID), "", stranger); got.Code != http.StatusNotFound {
		t.Errorf("foreign get status = %d, want 404", got.Code)
	}

	published := do(t, h, http.MethodPut, fmt.Sprintf("/api/teacher/activities/%d/status", activity.ID), `{"status":"published"}`, teacher)
	if published.Code != http.StatusOK {
		t.Fatalf("publish status = %d: %s", published.Code, published.Body)
	}
	if decodeActivity(t, published.Body.Bytes()).PublishedAt == nil {
		t.Error("published game has no published_at")
	}

	list := do(t, h, http.MethodGet, "/api/activities?age=4", "")
	if list.Code != http.StatusOK {
		t.Fatalf("public list status = %d: %s", list.Code, list.Body)
	}
	var page struct {
		Items   []activityResponse `json:"items"`
		HasMore bool               `json:"has_more"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Slug != activity.Slug || page.HasMore {
		t.Errorf("public list = %+v", page)
	}

	// Age outside the game's range hides it.
	outOfRange := do(t, h, http.MethodGet, "/api/activities?age=9", "")
	if err := json.Unmarshal(outOfRange.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(page.Items) != 0 {
		t.Errorf("age 9 returned %d games", len(page.Items))
	}

	// Hiding removes it from the catalog again.
	hidden := do(t, h, http.MethodPut, fmt.Sprintf("/api/teacher/activities/%d/status", activity.ID), `{"status":"hidden"}`, teacher)
	if hidden.Code != http.StatusOK {
		t.Fatalf("hide status = %d: %s", hidden.Code, hidden.Body)
	}
	if got := do(t, h, http.MethodGet, "/api/activities/"+activity.Slug, ""); got.Code != http.StatusNotFound {
		t.Errorf("hidden public status = %d, want 404", got.Code)
	}
}

func TestActivityRejectsBadInput(t *testing.T) {
	srv, _ := newTeacherServer(t)
	h := srv.Router()
	teacher := loginCookie(t, srv, "games@example.com", "teacher")

	for _, body := range []string{
		`{"title":"X","description":"d","embed_url":"https://evil.example/embed/abcdefgh","game_type":"Quiz"}`,
		`{"title":"X","description":"d","embed_url":"http://wordwall.net/embed/abcdefgh","game_type":"Quiz"}`,
		`{"title":"X","description":"d","embed_url":"https://wordwall.net/game/abcdefgh","game_type":"Quiz"}`,
		`{"title":"X","description":"d","embed_url":"https://wordwall.net/embed/abcdefgh"}`,
		`{"title":"","description":"d","embed_url":"https://wordwall.net/embed/abcdefgh","game_type":"Quiz"}`,
	} {
		if got := do(t, h, http.MethodPost, "/api/teacher/activities", body, teacher); got.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", body, got.Code)
		}
	}

	created := do(t, h, http.MethodPost, "/api/teacher/activities", activityJSON, teacher)
	activity := decodeActivity(t, created.Body.Bytes())

	// A published game may not lose its description.
	if got := do(t, h, http.MethodPut, fmt.Sprintf("/api/teacher/activities/%d/status", activity.ID), `{"status":"published"}`, teacher); got.Code != http.StatusOK {
		t.Fatalf("publish status = %d: %s", got.Code, got.Body)
	}
	cleared := `{"title":"Chủ đề động vật","description":"","embed_url":"https://wordwall.net/embed/629ec0e8ad7649f5b530f95fd81df87e","game_type":"Ghép đôi"}`
	if got := do(t, h, http.MethodPut, fmt.Sprintf("/api/teacher/activities/%d", activity.ID), cleared, teacher); got.Code != http.StatusBadRequest {
		t.Errorf("clearing description on a published game = %d, want 400", got.Code)
	}

	if got := do(t, h, http.MethodGet, "/api/activities?page=100000", ""); got.Code != http.StatusOK {
		t.Errorf("huge page = %d, want 200 (clamped)", got.Code)
	}
	if got := do(t, h, http.MethodGet, "/api/activities?age=99", ""); got.Code != http.StatusBadRequest {
		t.Errorf("bad age = %d, want 400", got.Code)
	}
	if got := do(t, h, http.MethodGet, "/api/teacher/activities", ""); got.Code != http.StatusUnauthorized {
		t.Errorf("anonymous teacher list = %d, want 401", got.Code)
	}
}

func TestActivityThumbnail(t *testing.T) {
	srv, store := newTeacherServer(t)
	h := srv.Router()
	teacher := loginCookie(t, srv, "games@example.com", "teacher")

	created := do(t, h, http.MethodPost, "/api/teacher/activities", activityJSON, teacher)
	activity := decodeActivity(t, created.Body.Bytes())

	upload := func(width, height int) *http.Response {
		body, contentType := multipartBody(t, nil, map[string]struct {
			contentType string
			data        []byte
		}{"thumbnail": {"image/png", pngImage(t, width, height)}})

		req, err := http.NewRequest(http.MethodPut,
			fmt.Sprintf("/api/teacher/activities/%d/thumbnail", activity.ID), body)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Content-Type", contentType)
		req.AddCookie(teacher)

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Result()
	}

	ok := upload(1200, 630)
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("thumbnail status = %d", ok.StatusCode)
	}
	keys := store.Keys()
	if len(keys) != 1 || !strings.HasPrefix(keys[0], fmt.Sprintf("activities/%d/thumbnail-", activity.ID)) || !strings.HasSuffix(keys[0], ".png") {
		t.Errorf("stored keys = %v", keys)
	}

	if square := upload(400, 400); square.StatusCode != http.StatusBadRequest {
		t.Errorf("square thumbnail status = %d, want 400", square.StatusCode)
	}
}
