package httpapi

import (
	"net/http"
	"time"

	"littleworld/internal/sales"
)

type salesItemResponse struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Sold       int64  `json:"sold"`
	RevenueVND int64  `json:"revenue_vnd"`
}

type salesTotalsResponse struct {
	Orders           int64 `json:"orders"`
	RevenueVND       int64 `json:"revenue_vnd"`
	BookRevenueVND   int64 `json:"book_revenue_vnd"`
	BundleRevenueVND int64 `json:"bundle_revenue_vnd"`
	BooksSold        int64 `json:"books_sold"`
	BundlesSold      int64 `json:"bundles_sold"`
}

// week_start is a plain date: the chart labels a bucket, it does not need a clock.
type salesWeekResponse struct {
	WeekStart  string `json:"week_start"`
	RevenueVND int64  `json:"revenue_vnd"`
}

type salesOrderResponse struct {
	PaidAt    time.Time `json:"paid_at"`
	Title     string    `json:"title"`
	AmountVND int64     `json:"amount_vnd"`
}

type salesResponse struct {
	Totals  salesTotalsResponse  `json:"totals"`
	Books   []salesItemResponse  `json:"books"`
	Bundles []salesItemResponse  `json:"bundles"`
	Trend   []salesWeekResponse  `json:"trend"`
	Recent  []salesOrderResponse `json:"recent"`
}

// salesRanges maps the period picker to a cut-off. "all" (and anything unset) means no
// cut-off, so the Teacher Studio dashboard keeps its all-time numbers.
func salesSince(rangeValue string, now time.Time) (*time.Time, bool) {
	switch rangeValue {
	case "", "all":
		return nil, true
	case "30d":
		since := now.AddDate(0, 0, -30)
		return &since, true
	case "90d":
		since := now.AddDate(0, 0, -90)
		return &since, true
	default:
		return nil, false
	}
}

func (s *Server) handleSales(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	since, ok := salesSince(r.URL.Query().Get("range"), time.Now())
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_range", "Khoảng thời gian không hợp lệ.")
		return
	}

	summary, err := s.Sales.Summary(r.Context(), user.ID, since)
	if err != nil {
		s.Log.Error("sales summary", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}

	// A teacher's revenue is private: a shared cache must never replay it.
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, salesResponse{
		Totals: salesTotalsResponse{
			Orders:           summary.Orders,
			RevenueVND:       summary.RevenueVND,
			BookRevenueVND:   summary.BookRevenueVND,
			BundleRevenueVND: summary.BundleRevenueVND,
			BooksSold:        summary.BooksSold,
			BundlesSold:      summary.BundlesSold,
		},
		Books:   salesItems(summary.Books),
		Bundles: salesItems(summary.Bundles),
		Trend:   salesWeeks(summary.Trend),
		Recent:  salesOrders(summary.Recent),
	})
}

func salesItems(items []sales.Item) []salesItemResponse {
	resp := make([]salesItemResponse, 0, len(items))
	for _, item := range items {
		resp = append(resp, salesItemResponse{
			ID:         item.ID,
			Title:      item.Title,
			Sold:       item.Sold,
			RevenueVND: item.RevenueVND,
		})
	}
	return resp
}

func salesWeeks(weeks []sales.Week) []salesWeekResponse {
	resp := make([]salesWeekResponse, 0, len(weeks))
	for _, week := range weeks {
		resp = append(resp, salesWeekResponse{
			WeekStart:  week.WeekStart.Format("2006-01-02"),
			RevenueVND: week.RevenueVND,
		})
	}
	return resp
}

func salesOrders(orders []sales.Order) []salesOrderResponse {
	resp := make([]salesOrderResponse, 0, len(orders))
	for _, order := range orders {
		resp = append(resp, salesOrderResponse{
			PaidAt:    order.PaidAt,
			Title:     order.Title,
			AmountVND: order.AmountVND,
		})
	}
	return resp
}
