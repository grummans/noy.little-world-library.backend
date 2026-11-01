package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"littleworld/internal/orders"
	"littleworld/internal/vnpay"
)

type orderResponse struct {
	ID            int64      `json:"id"`
	TxnRef        string     `json:"txn_ref"`
	Status        string     `json:"status"`
	AmountVND     int64      `json:"amount_vnd"`
	BookID        *int64     `json:"book_id,omitempty"`
	BundleID      *int64     `json:"bundle_id,omitempty"`
	PaidAt        *time.Time `json:"paid_at,omitempty"`
	PaymentURL    string     `json:"payment_url,omitempty"`
	ResponseCode  *string    `json:"response_code,omitempty"`
	TransactionNo *string    `json:"transaction_no,omitempty"`
}

func newOrderResponse(order orders.Order) orderResponse {
	return orderResponse{
		ID:            order.ID,
		TxnRef:        order.TxnRef,
		Status:        order.Status,
		AmountVND:     order.AmountVND,
		BookID:        order.BookID,
		BundleID:      order.BundleID,
		PaidAt:        order.PaidAt,
		ResponseCode:  order.ResponseCode,
		TransactionNo: order.TransactionNo,
	}
}

// POST /api/orders — buy one book or one bundle now (no cart).
func (s *Server) handleCreateOrder(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	var in struct {
		BookSlug   string `json:"book_slug"`
		BundleSlug string `json:"bundle_slug"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Malformed request body.")
		return
	}

	checkout, err := s.Orders.Create(r.Context(), user.ID, in.BookSlug, in.BundleSlug,
		s.VnpayReturnURL, clientIP(r))
	switch {
	case errors.Is(err, orders.ErrValidation):
		writeError(w, http.StatusBadRequest, "validation", err.Error())
	case errors.Is(err, orders.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Item not found.")
	case errors.Is(err, orders.ErrNotPurchasable):
		writeError(w, http.StatusConflict, "not_purchasable", "This item is not for sale.")
	case errors.Is(err, orders.ErrGateway):
		s.Log.Error("vnpay unavailable", "err", err)
		writeError(w, http.StatusServiceUnavailable, "payment_unavailable", "Payment is not available right now.")
	case err != nil:
		s.Log.Error("create order", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not start the payment.")
	default:
		resp := newOrderResponse(checkout.Order)
		resp.PaymentURL = checkout.PaymentURL
		writeJSON(w, http.StatusCreated, resp)
	}
}

// GET /api/orders/{txnRef} — the buyer polls their own order after returning from VNPay.
func (s *Server) handleGetOrder(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	order, err := s.Orders.Get(r.Context(), user.ID, r.PathValue("txnRef"))
	switch {
	case errors.Is(err, orders.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Order not found.")
	case err != nil:
		s.Log.Error("get order", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not load the order.")
	default:
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, newOrderResponse(order))
	}
}

// GET /api/payments/vnpay/return — browser lands here; it only reports state.
func (s *Server) handleVnpayReturn(w http.ResponseWriter, r *http.Request) {
	state := s.Orders.ReturnState(r.Context(), r.URL.Query())
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  state.Status,
		"code":    state.Code,
		"message": state.Message,
		"txn_ref": state.TxnRef,
	})
}

// GET /api/payments/vnpay/ipn — the only place an order becomes paid.
func (s *Server) handleVnpayIPN(w http.ResponseWriter, r *http.Request) {
	response := s.Orders.HandleIPN(r.Context(), r.URL.Query())
	writeJSON(w, http.StatusOK, map[string]string{
		"RspCode": response.RspCode,
		"Message": response.Message,
	})
}

// vnpayQuery re-exports the callbacks' parameter type for tests.
func vnpayQuery(r *http.Request) url.Values { return r.URL.Query() }

var _ = vnpay.SuccessCode
