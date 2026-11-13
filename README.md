# Little World Library — API
Go modular monolith. `cmd/` = wiring only; business logic, handlers, persistence and
integrations live in `internal/`. Không dùng dotenv: config chỉ đến từ environment.

## Chạy local

Postgres 16 và MinIO chạy trên server `.48`. Tạo `.env` từ `.env.example` rồi source:

```bash
# `source .env` vỡ nếu password có ! hoặc ) — đọc từng dòng thay vì source:
set -a; while IFS='=' read -r k v; do case "$k" in ''|\#*) continue;; esac; export "$k=$v"; done < .env; set +a

go run ./cmd/migrate up        # up | down | reset | status
go run ./cmd/seed              # dữ liệu demo: teacher/parent, 5 sách, 2 gói
go run ./cmd/api               # http://localhost:8080/healthz
go test ./...                  # test DB cần DATABASE_URL, thiếu thì skip
```

`APP_ENV` mặc định `prod` để cookie luôn `Secure`; đặt `APP_ENV=dev` khi chạy local.
Endpoint nhận JSON yêu cầu header `Content-Type: application/json` (curl: `-H '...'`).

## Migrations

- SQL nhúng bằng `go:embed` trong `internal/db/migrations/`, chạy bằng goose library
  (không cần cài goose CLI).
- Test tự tạo schema tạm (`mtest_<timestamp>`), chạy up/down rồi drop — không đụng dữ liệu dev. `down`/`reset` bị chặn nếu database không phải `little_world_dev`, trừ khi đặt `ALLOW_DESTRUCTIVE_MIGRATIONS=1`.
- Seed dev tách riêng khỏi chuỗi migration: `go run ./cmd/seed`; `reset` + seed chỉ dành cho môi trường dev.


## Pending

Playwright e2e tự động, xác minh đủ các kịch bản VNPay sandbox bằng credentials thực
(TODO mục 1, 6, 8).

## API

| Method | Path | Auth |
|---|---|---|
| GET | `/healthz` | không (ping DB + bucket nếu có S3) |
| POST | `/api/auth/register` | không — body `{display_name, email, password, role}`, trả kèm `display_name` |
| POST | `/api/auth/login` | không — set cookie `lw_session` |
| POST | `/api/auth/logout` | cookie (không có cookie vẫn trả 204) |
| GET | `/api/auth/me` | cookie |

Teacher Studio (`role=teacher`; sách của giáo viên khác trả 404, không phải 403):

| Method | Path | Ghi chú |
|---|---|---|
| GET | `/api/teacher/books` | danh sách sách của chính mình |
| POST | `/api/teacher/books` | multipart: field metadata + `epub` + `cover` (đều optional) |
| GET | `/api/teacher/books/{id}` | |
| PUT | `/api/teacher/books/{id}` | JSON metadata |
| PUT | `/api/teacher/books/{id}/files` | multipart, thay EPUB và/hoặc cover |
| POST | `/api/teacher/books/{id}/publish` | cần cover + EPUB + description |
| POST | `/api/teacher/books/{id}/unpublish` | về draft |
| GET | `/api/teacher/bundles` | danh sách gói của chính mình |
| POST | `/api/teacher/bundles` | multipart: metadata + `book_ids` (lặp lại được) + `cover` |
| GET | `/api/teacher/bundles/{id}` | |
| PUT | `/api/teacher/bundles/{id}` | multipart metadata + `book_ids`; cover đi endpoint riêng |
| PUT | `/api/teacher/bundles/{id}/cover` | multipart, chỉ field `cover` |
| POST | `/api/teacher/bundles/{id}/publish` | cần ≥ 1 sách paid + published của chính teacher |
| POST | `/api/teacher/bundles/{id}/unpublish` | về draft |
| GET | `/api/tags` | công khai, danh sách chủ đề để chọn |

Góc vui học (Wordwall; game tạo ngoài hệ thống, chỉ lưu metadata + embed URL):

| Method | Path | Ghi chú |
|---|---|---|
| GET | `/api/teacher/activities` | game của chính mình + `stats` (total/published/draft/hidden) |
| POST | `/api/teacher/activities` | JSON metadata game Wordwall; thumbnail đi endpoint riêng |
| GET / PUT | `/api/teacher/activities/{id}` | đọc/sửa metadata |
| PUT | `/api/teacher/activities/{id}/thumbnail` | multipart field `thumbnail`: PNG/JPEG/WebP gần 1.91:1 |
| PUT | `/api/teacher/activities/{id}/status` | `{status}`: `draft` / `hidden` / `published` (không moderation) |
| GET | `/api/activities` | công khai, `q`, `game_type`, `skill`, `age`, `page`, `limit`; chỉ game published |
| GET | `/api/activities/facets` | thể loại + kỹ năng đang có để dựng bộ lọc |
| GET | `/api/activities/{slug}` | 404 cho draft/hidden |

| POST | `/api/teacher/tags` | giáo viên tạo chủ đề: `{name}`, slug tự sinh |
| DELETE | `/api/teacher/tags/{id}` | chỉ xoá được khi chưa gán cho sách/gói nào |

Public storefront (chỉ thấy `status = 'published'`):

| Method | Path | Ghi chú |
|---|---|---|
| GET | `/api/books` | `kind=all\|book\|bundle`, `q`, `tag` (slug), `age` (0–18), `page`, `limit` (≤50) |
| GET | `/api/books/{slug}` | 404 cho draft |
| GET | `/api/bundles/{slug}` | kèm danh sách sách đã publish trong gói |
| GET | `/api/books/{slug}/read` | trả `{url, source, expires_in}`; free = ai cũng đọc, paid = cần order `paid` (trực tiếp hoặc qua bundle) |

Parent Library (`role=parent`):

| Method | Path | Ghi chú |
|---|---|---|
| GET | `/api/parent/library` | `{bundles, books, readable}`; bundle giữ card sản phẩm, sách trong bundle nằm ở `readable` với `origin.type = "bundle"` |

Thanh toán (VNPay sandbox; để trống `VNPAY_*` thì checkout trả 503):

| Method | Path | Ghi chú |
|---|---|---|
| POST | `/api/orders` | `{book_slug}` hoặc `{bundle_slug}` → `{txn_ref, payment_url, amount_vnd}`; giá lấy từ DB |
| GET | `/api/orders` | danh sách đơn của parent hiện tại |
| GET | `/api/orders/{txnRef}` | buyer poll trạng thái; order của người khác = 404 |
| GET | `/api/payments/vnpay/return` | xác thực callback, áp dụng success hợp lệ và trả `paid / verifying / failed / cancelled / invalid` |
| GET | `/api/payments/vnpay/ipn` | xác thực server-to-server, cấp quyền idempotent; trả `{RspCode, Message}` |

IPN URL **không** nằm trong email VNPay gửi: phải khai trên merchant portal
(`https://sandbox.vnpayment.vn/merchantv2/Account/TerminalEdit.htm` cho sandbox; production
thì liên hệ VNPay). URL IPN bắt buộc HTTPS.
IPN xác thực chữ ký, `vnp_TmnCode`, `vnp_Amount = amount_vnd * 100` và `vnp_TransactionStatus = 00` trước khi chuyển order sang `paid`; lặp IPN trả `02`, không cấp quyền lần hai. Return URL cũng xác thực toàn bộ callback trước khi áp dụng giao dịch thành công, nên không tin dữ liệu query thô từ browser.

Cover trả về dạng presigned URL 15 phút trong `cover_url`; ký thất bại thì field rỗng
(chợ ảnh placeholder), không làm hỏng endpoint. `book_count` của gói chỉ đếm sách
published + paid. Tìm kiếm không dấu hoạt động vì `q` được chuẩn hoá thành slug trước
khi so khớp (`Động vật` → `dong-vat`).

Upload đi qua API (không presigned PUT): file ghi vào `uploads/<random>`, được kiểm tra
rồi `CopyObject` sang key chính. Nhờ vậy upload hỏng không làm hỏng sách đang bán.

Session là opaque token 32 byte, DB chỉ lưu SHA-256; cookie HttpOnly + SameSite=Lax,
`Secure` bật khi `APP_ENV != dev`. Xoá session hết hạn là việc của job định kỳ (chưa làm).
## Cấu trúc code (Go, không theo tầng Java)

Go tổ chức theo **package = một tính năng**, không theo lớp Controller/Service/Repository.
Không có `domain/`, `usecase/`, `repository/`; handler HTTP nằm cùng repo với service vì
chúng cùng một tính năng.

Mỗi package một mức trách nhiệm, tách **file** khi vượt ~300 dòng hoặc có từ 3 trách nhiệm:

```
internal/books/
  book.go      # package doc + entity (Book, Metadata, Upload) + interface + lỗi
  validate.go  # chuẩn hoá + validate metadata + Slugify
  store.go     # SQL: query, scan, map lỗi DB
  service.go   # use case: Create/Update/Publish/... + upload staging
```

Quy ước: entity/interface/SQL không export ra ngoài package; `httpapi` chỉ gọi service,
không tự viết SQL. `cmd/` chỉ wiring.


## Object storage

MinIO trên `.48`, bucket private `little-world`. Object key cố định theo id để thay EPUB
là ghi đè tại chỗ (người đã mua luôn đọc bản mới nhất):

```
books/<id>/book.epub      books/<id>/cover.{jpg,png}
bundles/<id>/cover.{jpg,png}
```

Hai endpoint vì chữ ký S3 gắn với `Host` của URL:

- `S3_ENDPOINT` — API gọi MinIO hiện có trên `.48` (`http://192.168.1.48:9000` ở staging/dev).
- `S3_PUBLIC_ENDPOINT` — host nhúng vào presigned URL, phải là host browser dùng được
  (`https://media.littleworld.cloud` khi lên prod). Presigned GET hết hạn sau 15 phút.
- Thiếu cấu hình S3 thì API vẫn chạy (auth/catalog), `storage.Client` = nil, chưa có upload.
