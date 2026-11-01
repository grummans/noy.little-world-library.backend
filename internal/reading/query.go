package reading

// Access is decided in one query: the book row plus whether the caller bought it
// directly or through a bundle. A missing user id (0) buys nothing.
const accessQuery = `
SELECT b.id, b.access_type, b.epub_object_key,
       EXISTS (
           SELECT 1 FROM orders o
           WHERE o.buyer_id = $2 AND o.book_id = b.id AND o.status = 'paid'
       ) AS bought_directly,
       EXISTS (
           SELECT 1 FROM orders o
           JOIN bundle_books bb ON bb.bundle_id = o.bundle_id
           WHERE o.buyer_id = $2 AND o.status = 'paid' AND bb.book_id = b.id
       ) AS bought_in_bundle
FROM books b
WHERE b.slug = $1 AND b.status = 'published'`

// Purchase access is derived, not snapshotted: an order plus the bundle's current book
// list. SCHEMA.md §5 defines it this way, and a purchase outlives an unpublish — the
// seller stops selling, buyers keep reading. Creating an order for an unpublished item
// is therefore blocked at checkout (VNPay step), not here.
