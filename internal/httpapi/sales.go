package httpapi

import (
	"net/http"

	"littleworld/internal/sales"
)

type salesItemResponse struct {
	ID         int64 `json:"id"`
	Sold       int64 `json:"sold"`
	RevenueVND int64 `json:"revenue_vnd"`
}

type salesTotalsResponse struct {
	Orders           int64 `json:"orders"`
	RevenueVND       int64 `json:"revenue_vnd"`
	BookRevenueVND   int64 `json:"book_revenue_vnd"`
	BundleRevenueVND int64 `json:"bundle_revenue_vnd"`
}

type salesResponse struct {
	Totals  salesTotalsResponse `json:"totals"`
	Books   []salesItemResponse `json:"books"`
	Bundles []salesItemResponse `json:"bundles"`
}

func (s *Server) handleSales(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	summary, err := s.Sales.Summary(r.Context(), user.ID)
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
		},
		Books:   salesItems(summary.Books),
		Bundles: salesItems(summary.Bundles),
	})
}

func salesItems(items []sales.Item) []salesItemResponse {
	resp := make([]salesItemResponse, 0, len(items))
	for _, item := range items {
		resp = append(resp, salesItemResponse{
			ID:         item.ID,
			Sold:       item.Sold,
			RevenueVND: item.RevenueVND,
		})
	}
	return resp
}
