package library

// Every query returns the shared catalog card columns plus whatever the shelf needs,
// so a book looks the same in the library as in the storefront. Purchases can repeat
// (the same product bought twice), so each product is reduced to its latest paid order.

const bundleBookCount = `(SELECT count(*) FROM bundle_books bb2
	JOIN books cb ON cb.id = bb2.book_id
	WHERE bb2.bundle_id = bu.id AND cb.status = 'published'
	  AND cb.access_type = 'paid' AND cb.price_vnd > 0)`

const bundleTags = `COALESCE((SELECT array_agg(DISTINCT t.slug ORDER BY t.slug)
	FROM bundle_books bb JOIN book_tags bt ON bt.book_id = bb.book_id
	JOIN tags t ON t.id = bt.tag_id
	WHERE bb.bundle_id = bu.id), '{}')`

const bookTags = `COALESCE((SELECT array_agg(t.slug ORDER BY t.slug)
	FROM book_tags bt JOIN tags t ON t.id = bt.tag_id
	WHERE bt.book_id = b.id), '{}')`

var purchasedBundlesQuery = `
SELECT kind, id, slug, title, cover_object_key, access_type, price_vnd, age_min, age_max,
       page_count, has_audio, has_video, published_at, book_count, tag_slugs, paid_at
FROM (
	SELECT DISTINCT ON (bu.id)
	       'bundle' AS kind, bu.id, bu.slug, bu.title, bu.cover_object_key, 'paid' AS access_type,
	       bu.price_vnd, NULL::smallint AS age_min, NULL::smallint AS age_max,
	       NULL::smallint AS page_count, false AS has_audio, false AS has_video, bu.published_at,
	       ` + bundleBookCount + ` AS book_count,
	       ` + bundleTags + ` AS tag_slugs,
	       o.paid_at
	FROM orders o
	JOIN bundles bu ON bu.id = o.bundle_id
	WHERE o.buyer_id = $1 AND o.status = 'paid'
	ORDER BY bu.id, o.paid_at DESC NULLS LAST
) purchased
ORDER BY paid_at DESC NULLS LAST, id DESC`

var directBooksQuery = `
SELECT kind, id, slug, title, cover_object_key, access_type, price_vnd, age_min, age_max,
       page_count, has_audio, has_video, published_at, book_count, tag_slugs, paid_at
FROM (
	SELECT DISTINCT ON (b.id)
	       'book' AS kind, b.id, b.slug, b.title, b.cover_object_key, b.access_type, b.price_vnd,
	       b.age_min, b.age_max, b.page_count, b.has_audio, b.has_video, b.published_at, 1 AS book_count,
	       ` + bookTags + ` AS tag_slugs,
	       o.paid_at
	FROM orders o
	JOIN books b ON b.id = o.book_id
	WHERE o.buyer_id = $1 AND o.status = 'paid' AND b.status = 'published'
	ORDER BY b.id, o.paid_at DESC NULLS LAST
) purchased
ORDER BY paid_at DESC NULLS LAST, id DESC`

var bundleBooksQuery = `
SELECT bundle_id, position, kind, id, slug, title, cover_object_key, access_type, price_vnd,
       age_min, age_max, page_count, has_audio, has_video, published_at, book_count, tag_slugs
FROM (
	SELECT DISTINCT ON (bb.bundle_id, b.id)
	       bb.bundle_id, bb.position,
	       'book' AS kind, b.id, b.slug, b.title, b.cover_object_key, b.access_type, b.price_vnd,
	       b.age_min, b.age_max, b.page_count, b.has_audio, b.has_video, b.published_at,
	       1 AS book_count,
	       ` + bookTags + ` AS tag_slugs
	FROM orders o
	JOIN bundle_books bb ON bb.bundle_id = o.bundle_id
	JOIN books b ON b.id = bb.book_id
	WHERE o.buyer_id = $1 AND o.status = 'paid' AND b.status = 'published'
	ORDER BY bb.bundle_id, b.id, bb.position
) rows
ORDER BY bundle_id, position`
