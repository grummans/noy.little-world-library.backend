package orders

// Only a verified IPN may move an order to paid; the browser Return is read-only.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"littleworld/internal/vnpay"
)

// IPNResponse is the exact body VNPay expects back.
type IPNResponse struct {
	RspCode string
	Message string
}

// HandleIPN verifies the callback, compares it with the stored order, and grants access
// at most once. VNPay retries until it sees RspCode 00 or 02, so every branch must be
// idempotent.
func (s *Service) HandleIPN(ctx context.Context, query url.Values) IPNResponse {
	result, err := s.gateway.Verify(query)
	switch {
	case errors.Is(err, vnpay.ErrInvalidSignature):
		return IPNResponse{RspInvalidSign, "Invalid signature"}
	case errors.Is(err, vnpay.ErrNotConfigured):
		return IPNResponse{RspUnknownError, "Gateway not configured"}
	case err != nil:
		return IPNResponse{RspUnknownError, "Unknown error"}
	}

	// A browser Return payload carries no transaction status; only a real IPN may
	// confirm a payment, so anything without it is refused here.
	if result.TransactionStatus == "" {
		return IPNResponse{RspUnknownError, "Missing transaction status"}
	}

	order, err := s.byTxnRef(ctx, result.TxnRef)
	if errors.Is(err, ErrNotFound) {
		return IPNResponse{RspOrderNotFound, "Order not found"}
	}
	if err != nil {
		return IPNResponse{RspUnknownError, "Unknown error"}
	}

	// Compare the exact scaled amount so a mismatch cannot round to the same VND.
	if result.AmountRaw != order.AmountVND*100 {
		return IPNResponse{RspInvalidAmount, "Invalid amount"}
	}
	if order.Status == StatusPaid {
		return IPNResponse{RspAlreadyPaid, "Order already confirmed"}
	}

	status := StatusPaid
	if !result.Confirmed {
		status = statusForCode(result.ResponseCode)
	}

	applied, err := s.applyResult(ctx, order.ID, status, result)
	if err != nil {
		return IPNResponse{RspUnknownError, "Unknown error"}
	}
	if !applied {
		// Another IPN won the race: report success so VNPay stops retrying.
		return IPNResponse{RspAlreadyPaid, "Order already confirmed"}
	}
	return IPNResponse{RspSuccess, "Confirm Success"}
}

// applyResult writes the outcome once. The WHERE status = 'pending' guard makes a
// duplicate callback a no-op instead of a second grant.
func (s *Service) applyResult(ctx context.Context, orderID int64, status string, result vnpay.Result) (bool, error) {
	var (
		paidAt any
		txnNo  any
	)
	if status == StatusPaid {
		paidAt = s.now()
		txnNo = nilIfEmpty(result.TransactionNo)
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE orders
		 SET status = $1, vnpay_response_code = $2, vnpay_transaction_no = $3,
		     paid_at = CASE WHEN $1 = 'paid' THEN $4 ELSE paid_at END
		 WHERE id = $5 AND status = 'pending'`,
		status, result.ResponseCode, txnNo, paidAt, orderID,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Same VNPay transaction already recorded on another order row.
			return false, nil
		}
		return false, fmt.Errorf("apply payment result: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("apply payment result: %w", err)
	}
	return affected > 0, nil
}

// ReturnState is what the return page shows. It never grants access; at most it reports
// that a verified IPN already did.
type ReturnState struct {
	Status  string // paid | verifying | failed | cancelled | invalid
	Code    string
	Message string
	TxnRef  string
}

func (s *Service) ReturnState(ctx context.Context, query url.Values) ReturnState {
	result, err := s.gateway.Verify(query)
	if err != nil {
		return ReturnState{Status: "invalid", Message: "Chữ ký không hợp lệ."}
	}

	order, err := s.byTxnRef(ctx, result.TxnRef)
	if err != nil {
		return ReturnState{Status: "invalid", Message: "Không tìm thấy đơn hàng."}
	}

	state := ReturnState{Code: result.ResponseCode, TxnRef: order.TxnRef}
	switch order.Status {
	case StatusPaid:
		state.Status = "paid"
		state.Message = "Thanh toán thành công."
	case StatusCancelled:
		state.Status = "cancelled"
		state.Message = messageForCode(result.ResponseCode)
	case StatusFailed:
		state.Status = "failed"
		state.Message = messageForCode(result.ResponseCode)
	default:
		// IPN has not arrived yet: the page must keep polling or wait.
		state.Status = "verifying"
		state.Message = "Đang xác nhận thanh toán..."
	}
	return state
}

func (s *Service) byTxnRef(ctx context.Context, txnRef string) (Order, error) {
	if txnRef == "" {
		return Order{}, ErrNotFound
	}

	var (
		order    Order
		txnNo    sql.NullString
		code     sql.NullString
		paidAt   sql.NullTime
		bookID   sql.NullInt64
		bundleID sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, buyer_id, book_id, bundle_id, amount_vnd, status, vnpay_txn_ref,
		        vnpay_transaction_no, vnpay_response_code, paid_at, created_at
		 FROM orders WHERE vnpay_txn_ref = $1`, txnRef,
	).Scan(&order.ID, &order.BuyerID, &bookID, &bundleID, &order.AmountVND, &order.Status,
		&order.TxnRef, &txnNo, &code, &paidAt, &order.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, fmt.Errorf("read order: %w", err)
	}

	if bookID.Valid {
		order.BookID = &bookID.Int64
	}
	if bundleID.Valid {
		order.BundleID = &bundleID.Int64
	}
	if txnNo.Valid {
		order.TransactionNo = &txnNo.String
	}
	if code.Valid {
		order.ResponseCode = &code.String
	}
	if paidAt.Valid {
		order.PaidAt = &paidAt.Time
	}
	return order, nil
}

// Get returns one order for its owner; another buyer's order is "not found".
func (s *Service) Get(ctx context.Context, buyerID int64, txnRef string) (Order, error) {
	order, err := s.byTxnRef(ctx, txnRef)
	if err != nil {
		return Order{}, err
	}
	if order.BuyerID != buyerID {
		return Order{}, ErrNotFound
	}
	return order, nil
}

func statusForCode(code string) string {
	if code == codeCancelled {
		return StatusCancelled
	}
	return StatusFailed
}

func messageForCode(code string) string {
	switch code {
	case codeCancelled:
		return "Bạn đã huỷ thanh toán."
	case "11":
		return "Đã hết thời gian thanh toán. Vui lòng thử lại."
	case "51":
		return "Tài khoản không đủ số dư."
	case "09":
		return "Thẻ chưa đăng ký dịch vụ thanh toán."
	case "12":
		return "Thẻ đang bị khoá."
	case "13":
		return "Sai mật khẩu xác thực (OTP)."
	default:
		return "Thanh toán không thành công."
	}
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
