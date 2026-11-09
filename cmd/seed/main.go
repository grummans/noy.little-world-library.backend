// Command seed fills a fresh dev database with demo content so every screen has
// something real: one teacher, one parent, topics, free and paid books, and two bundles.
//
//	go run ./cmd/seed            # seed (fails if the teacher already exists)
//	go run ./cmd/seed --wipe-s3  # also empty the media bucket first
package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"littleworld/internal/activities"
	"littleworld/internal/auth"
	"littleworld/internal/books"
	"littleworld/internal/bundles"
	"littleworld/internal/config"
	"littleworld/internal/slug"
	"littleworld/internal/storage"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	wipe := flag.Bool("wipe-s3", false, "delete every object in the media bucket first")
	flag.Parse()

	if err := run(*wipe); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func run(wipe bool) error {
	cfg := config.Load()
	if cfg.DatabaseURL == "" {
		return errors.New("DATABASE_URL is empty")
	}

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	store, err := storage.New(cfg.S3)
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}

	ctx := context.Background()
	if wipe {
		// Emptying the media bucket in a non-dev environment is almost always a mistake.
		if cfg.AppEnv != "dev" && os.Getenv("ALLOW_DESTRUCTIVE_MIGRATIONS") != "1" {
			return fmt.Errorf("refusing to wipe the bucket with APP_ENV=%q", cfg.AppEnv)
		}
		if err := store.DeletePrefix(ctx, ""); err != nil {
			return fmt.Errorf("wipe bucket: %w", err)
		}
		fmt.Println("bucket emptied")
	}

	teacherID, err := ensureUser(ctx, db, "Cô giáo Linh", "teacher@littleworld.vn", auth.RoleTeacher)
	if err != nil {
		return err
	}
	parentID, err := ensureUser(ctx, db, "Phụ huynh Demo", "parent@littleworld.vn", auth.RoleParent)
	if err != nil {
		return err
	}

	bookSvc := books.New(db, store)
	bundleSvc := bundles.New(db, store)

	dongVat, err := ensureTag(ctx, db, "Động vật")
	if err != nil {
		return err
	}
	thienNhien, err := ensureTag(ctx, db, "Thiên nhiên")
	if err != nil {
		return err
	}
	if _, err := ensureTag(ctx, db, "Cảm xúc"); err != nil {
		return err
	}

	type seedBook struct {
		title    string
		desc     string
		access   string
		price    *int64
		tag      int64
		ageMin   int16
		ageMax   int16
		slugFile string
		publish  bool
	}

	at := func(v int64) *int64 { return &v }
	seeds := []seedBook{
		{"Bác Hồ với thiếu nhi", "Câu chuyện nhỏ về Bác dành cho các bạn nhỏ.", books.AccessFree, nil, dongVat, 3, 4, "bac-ho-voi-thieu-nhi", true},
		{"Động vật quanh bé", "Bé làm quen các bạn vật nuôi và thú rừng gần gũi.", books.AccessPaid, at(99000), dongVat, 3, 4, "dong-vat-quanh-be", true},
		{"Thế giới thực vật", "Hành trình của hạt mầm thành cây xanh.", books.AccessPaid, at(129000), thienNhien, 4, 6, "the-gioi-thuc-vat", true},
		{"Nước và hiện tượng tự nhiên", "Mưa, nắng và những điều kỳ diệu của nước.", books.AccessPaid, at(119000), thienNhien, 4, 6, "nuoc-va-hien-tuong-tu-nhien", true},
		{"Bản thân của bé", "Bản nháp để minh hoạ trạng thái chưa xuất bản.", books.AccessFree, nil, dongVat, 3, 4, "ban-than-cua-be", false},
	}

	created := map[string]int64{}
	for _, s := range seeds {
		bookID, err := createBook(ctx, bookSvc, teacherID, s.title, s.desc, s.access, s.price, s.tag, s.ageMin, s.ageMax, s.slugFile, s.publish)
		if err != nil {
			return fmt.Errorf("%s: %w", s.title, err)
		}
		created[s.slugFile] = bookID
		fmt.Printf("book  %-32s %s\n", s.title, map[bool]string{true: "published", false: "draft"}[s.publish])
	}

	bundleSeeds := []struct {
		title string
		desc  string
		price int64
		books []int64
	}{
		{"Bé yêu thiên nhiên", "Ba chuyến phiêu lưu nhỏ giúp bé quan sát và yêu quý thế giới tự nhiên.",
			150000, []int64{created["dong-vat-quanh-be"], created["the-gioi-thuc-vat"], created["nuoc-va-hien-tuong-tu-nhien"]}},
		// Bundles only hold published PAID books, so the free title stays out.
		{"Khám phá quanh bé", "Bộ đôi dành cho bé mới bắt đầu đọc.",
			99000, []int64{created["dong-vat-quanh-be"], created["the-gioi-thuc-vat"]}},
	}

	var firstBundleID int64
	for _, b := range bundleSeeds {
		bundleID, err := createBundle(ctx, db, bundleSvc, teacherID, b.title, b.desc, b.price, b.books)
		if err != nil {
			return fmt.Errorf("%s: %w", b.title, err)
		}
		if firstBundleID == 0 {
			firstBundleID = bundleID
		}
		fmt.Printf("bundle %-31s published\n", b.title)
	}

	// The two games the public Góc vui học page shows; the game itself lives on Wordwall.
	activitySvc := activities.New(db, store)
	activitySeeds := []struct {
		title    string
		desc     string
		gameType string
		topic    string
		skills   []string
		ageMin   int16
		ageMax   int16
		embedURL string
	}{
		{"Chủ đề động vật",
			"Bé nhận biết các con vật quen thuộc qua một trò chơi tương tác.",
			"Ghép đôi", "Động vật", []string{"Nhận biết", "Từ vựng"}, 3, 5,
			"https://wordwall.net/embed/629ec0e8ad7649f5b530f95fd81df87e?themeId=1&templateId=3&fontStackId=0"},
		{"Chủ đề thế giới thực vật",
			"Cùng bé khám phá cây cối và các loài hoa trong vườn.",
			"Ghép đôi", "Thực vật", []string{"Quan sát", "Từ vựng"}, 4, 6,
			"https://wordwall.net/embed/9dc14e870286476f9c37b78965bad24d?themeId=1&templateId=3&fontStackId=0"},
	}
	for _, a := range activitySeeds {
		ageMin, ageMax := a.ageMin, a.ageMax
		seeded, err := activitySvc.Create(ctx, teacherID, activities.Metadata{
			Title:       a.title,
			Description: a.desc,
			EmbedURL:    a.embedURL,
			GameType:    a.gameType,
			Topic:       a.topic,
			Skills:      a.skills,
			AgeMin:      &ageMin,
			AgeMax:      &ageMax,
		})
		if err != nil {
			return fmt.Errorf("%s: %w", a.title, err)
		}
		if _, err := activitySvc.SetStatus(ctx, teacherID, seeded.ID, activities.StatusPublished); err != nil {
			return fmt.Errorf("%s: %w", a.title, err)
		}
		fmt.Printf("game   %-32s published\n", a.title)
	}

	// One paid order so the parent shelf has content on first login.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO orders (buyer_id, book_id, amount_vnd, status, vnpay_txn_ref, paid_at)
		 VALUES ($1, $2, 99000, 'paid', 'seed-paid-book', now())
		 ON CONFLICT DO NOTHING`,
		parentID, created["dong-vat-quanh-be"],
	); err != nil {
		return fmt.Errorf("seed order: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO orders (buyer_id, bundle_id, amount_vnd, status, vnpay_txn_ref, paid_at)
		 VALUES ($1, $2, 150000, 'paid', 'seed-paid-bundle', now())
		 ON CONFLICT DO NOTHING`,
		parentID, firstBundleID,
	); err != nil {
		return fmt.Errorf("seed bundle order: %w", err)
	}

	fmt.Println()
	fmt.Println("teacher  teacher@littleworld.vn / password123")
	fmt.Println("parent   parent@littleworld.vn / password123")
	fmt.Println("seeded at", time.Now().Format("15:04:05"))
	return nil
}

func ensureUser(ctx context.Context, db *sql.DB, displayName, email, role string) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	svc := auth.New(db)
	user, err := svc.Register(ctx, displayName, email, "password123", role)
	if err != nil {
		return 0, fmt.Errorf("register %s: %w", email, err)
	}
	return user.ID, nil
}

func ensureTag(ctx context.Context, db *sql.DB, name string) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx, `SELECT id FROM tags WHERE name = $1`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO tags (slug, name) VALUES ($1, $2) RETURNING id`,
		slug.Make(name), name).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func createBook(ctx context.Context, svc *books.Service, ownerID int64, title, desc, access string, price *int64, tagID int64, ageMin, ageMax int16, fileBase string, publish bool) (int64, error) {
	epub, err := openUpload(assetPath(fileBase + ".epub"))
	if err != nil {
		return 0, err
	}
	cover, err := openUpload(assetPath(fileBase + ".png"))
	if err != nil {
		return 0, err
	}

	book, err := svc.Create(ctx, ownerID, books.CreateInput{
		Metadata: books.Metadata{
			Title:       title,
			Description: desc,
			AccessType:  access,
			PriceVND:    price,
			AgeMin:      &ageMin,
			AgeMax:      &ageMax,
			TagID:       &tagID,
		},
		Epub:  epub,
		Cover: cover,
	})
	if err != nil {
		return 0, err
	}

	if publish {
		if _, err := svc.Publish(ctx, ownerID, book.ID); err != nil {
			return 0, err
		}
	}
	return book.ID, nil
}

func createBundle(ctx context.Context, db *sql.DB, svc *bundles.Service, ownerID int64, title, desc string, price int64, bookIDs []int64) (int64, error) {
	bundle, err := svc.Create(ctx, ownerID, bundles.CreateInput{
		Metadata: bundles.Metadata{Title: title, Description: desc, PriceVND: &price},
		BookIDs:  bookIDs,
	})
	if err != nil {
		return 0, err
	}
	// A bundle card shows one representative cover: the first book's.
	if _, err := db.ExecContext(ctx,
		`UPDATE bundles SET cover_object_key = (SELECT cover_object_key FROM books WHERE id = $1) WHERE id = $2`,
		bookIDs[0], bundle.ID,
	); err != nil {
		return 0, err
	}
	if _, err := svc.Publish(ctx, ownerID, bundle.ID); err != nil {
		return 0, err
	}
	return bundle.ID, nil
}

// assets live in the frontend repo, next to the screens that show them.
func assetPath(name string) string {
	base := os.Getenv("SEED_ASSETS")
	if base == "" {
		base = filepath.Join("..", "noy.little_world_library.frontend", "public")
	}
	dir := "books"
	if filepath.Ext(name) == ".png" {
		dir = "covers"
	}
	return filepath.Join(base, dir, name)
}

func openUpload(path string) (*books.Upload, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	contentType := "application/epub+zip"
	if filepath.Ext(path) == ".png" {
		contentType = "image/png"
	}
	return &books.Upload{
		ContentType: contentType,
		Size:        int64(len(data)),
		Body:        bytes.NewReader(data),
	}, nil
}
