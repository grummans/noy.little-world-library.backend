package catalog

// The storefront JPQL-free SQL. Every query filters status = 'published'; an
// unpublished draft must never be reachable from a public endpoint.

import (
	"database/sql"
	"fmt"
)

// CardColumns is the column list ScanCard expects.
const CardColumns = `kind, id, slug, title, cover_object_key, access_type, price_vnd,
	age_min, age_max, page_count, has_audio, has_video, published_at, book_count, tag_slugs`

// $1 kind, $2 search as slug, $3 raw search, $4 tag slug, $5 age,
// $6 limit, $7 offset.
const listQuery = `
WITH items AS (
	SELECT 'book' AS kind, b.id, b.slug, b.title, b.cover_object_key, b.access_type,
	       b.price_vnd, b.age_min, b.age_max, b.page_count, b.has_audio, b.has_video,
	       b.published_at, 1 AS book_count,
	       COALESCE((SELECT array_agg(t.slug ORDER BY t.slug)
	                 FROM book_tags bt JOIN tags t ON t.id = bt.tag_id
	                 WHERE bt.book_id = b.id), '{}') AS tag_slugs
	FROM books b
	WHERE b.status = 'published'
	  AND ($1 = 'all' OR $1 = 'book')
	  AND ($3 = '' OR b.title ILIKE '%' || $3 || '%' ESCAPE '\'
	       OR ($2 <> '' AND b.slug LIKE '%' || $2 || '%' ESCAPE '\'))
	  AND ($4 = '' OR EXISTS (
	        SELECT 1 FROM book_tags bt2 JOIN tags t2 ON t2.id = bt2.tag_id
	        WHERE bt2.book_id = b.id AND t2.slug = $4))
	  -- COALESCE keeps a book with only one bound visible in its real range.
	  AND ($5::smallint IS NULL
	       OR (COALESCE(b.age_min, 0) <= $5 AND COALESCE(b.age_max, 18) >= $5))

	UNION ALL

	SELECT 'bundle' AS kind, bu.id, bu.slug, bu.title, bu.cover_object_key, 'paid',
	       bu.price_vnd, NULL::smallint, NULL::smallint, NULL::smallint, false, false,
	       bu.published_at,
	       (SELECT count(*) FROM bundle_books bb
	         JOIN books cb ON cb.id = bb.book_id
	        WHERE bb.bundle_id = bu.id AND cb.status = 'published'
	          AND cb.access_type = 'paid' AND cb.price_vnd > 0),
	       COALESCE((SELECT array_agg(t.slug ORDER BY t.slug)
	                 FROM bundle_tags bt JOIN tags t ON t.id = bt.tag_id
	                 WHERE bt.bundle_id = bu.id), '{}')
	FROM bundles bu
	WHERE bu.status = 'published'
	  AND ($1 = 'all' OR $1 = 'bundle')
	  AND ($3 = '' OR bu.title ILIKE '%' || $3 || '%' ESCAPE '\'
	       OR ($2 <> '' AND bu.slug LIKE '%' || $2 || '%' ESCAPE '\'))
	  AND ($4 = '' OR EXISTS (
	        SELECT 1 FROM bundle_tags bt2 JOIN tags t2 ON t2.id = bt2.tag_id
	        WHERE bt2.bundle_id = bu.id AND t2.slug = $4))
	  -- Bundles carry no age range, so an age filter hides them.
	  AND $5::smallint IS NULL
)
SELECT ` + CardColumns + `
FROM items
ORDER BY published_at DESC, kind, id DESC
LIMIT $6 OFFSET $7`

const bookDetailQuery = `
SELECT b.id, b.slug, b.title, b.description, b.cover_object_key, b.access_type, b.price_vnd,
       b.age_min, b.age_max, b.page_count, b.has_audio, b.has_video, b.published_at,
       COALESCE((SELECT array_agg(t.slug ORDER BY t.slug)
                 FROM book_tags bt JOIN tags t ON t.id = bt.tag_id
                 WHERE bt.book_id = b.id), '{}'),
       b.epub_object_key IS NOT NULL
FROM books b
WHERE b.slug = $1 AND b.status = 'published'`

const bundleDetailQuery = `
SELECT bu.id, bu.slug, bu.title, bu.description, bu.cover_object_key, bu.price_vnd, bu.published_at,
       COALESCE((SELECT array_agg(t.slug ORDER BY t.slug)
                 FROM bundle_tags bt JOIN tags t ON t.id = bt.tag_id
                 WHERE bt.bundle_id = bu.id), '{}'),
       (SELECT count(*) FROM bundle_books bb
         JOIN books cb ON cb.id = bb.book_id
        WHERE bb.bundle_id = bu.id AND cb.status = 'published'
          AND cb.access_type = 'paid' AND cb.price_vnd > 0)

FROM bundles bu
WHERE bu.slug = $1 AND bu.status = 'published'`

// Only published books inside a bundle are shown, in the teacher's order.
const bundleBooksQuery = `
SELECT 'book', b.id, b.slug, b.title, b.cover_object_key, b.access_type, b.price_vnd,
       b.age_min, b.age_max, b.page_count, b.has_audio, b.has_video, b.published_at, 1,
       COALESCE((SELECT array_agg(t.slug ORDER BY t.slug)
                 FROM book_tags bt JOIN tags t ON t.id = bt.tag_id
                 WHERE bt.book_id = b.id), '{}')
FROM bundle_books bb
JOIN books b ON b.id = bb.book_id
WHERE bb.bundle_id = $1 AND b.status = 'published'
ORDER BY bb.position`

type rowScanner interface {
	Scan(dest ...any) error
}

// ScanCard reads one row in the shared card column order; other packages reuse it so
// their JSON matches the storefront exactly.
func ScanCard(row rowScanner) (Card, error) {
	var (
		card      Card
		cover     sql.NullString
		price     sql.NullInt64
		ageMin    sql.NullInt64
		ageMax    sql.NullInt64
		publish   sql.NullTime
		pageCount sql.NullInt64
	)
	if err := row.Scan(&card.Kind, &card.ID, &card.Slug, &card.Title, &cover,
		&card.AccessType, &price, &ageMin, &ageMax, &pageCount, &card.HasAudio, &card.HasVideo,
		&publish, &card.BookCount, &card.TagSlugs); err != nil {
		return Card{}, fmt.Errorf("scan card: %w", err)
	}

	card.CoverKey = cover.String
	if price.Valid {
		value := price.Int64
		card.PriceVND = &value
	}
	if ageMin.Valid {
		value := int16(ageMin.Int64)
		card.AgeMin = &value
	}
	if ageMax.Valid {
		value := int16(ageMax.Int64)
		card.AgeMax = &value
	}
	if pageCount.Valid {
		value := int16(pageCount.Int64)
		card.PageCount = &value
	}
	if publish.Valid {
		card.PublishedAt = publish.Time
	}
	return card, nil
}
