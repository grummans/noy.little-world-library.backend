package vnpay

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testClient() *Client {
	return New(Config{
		TmnCode:    "TESTCODE",
		HashSecret: "SECRETKEY1234567890",
		PaymentURL: "https://sandbox.vnpayment.vn/paymentv2/vpcpay.html",
	})
}

func TestBuildURLIsSigned(t *testing.T) {
	client := testClient()

	signed, err := client.BuildURL(PaymentRequest{
		TxnRef:     "LW1",
		AmountVND:  99000,
		OrderInfo:  "Thanh toan sach",
		ReturnURL:  "https://example.com/return",
		ClientIP:   "203.0.113.7",
		CreateDate: time.Date(2026, 11, 1, 10, 30, 0, 0, vnLocation),
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	query := parsed.Query()

	if got := query.Get("vnp_Amount"); got != "9900000" {
		t.Errorf("vnp_Amount = %q, want amount*100", got)
	}
	if got := query.Get("vnp_CreateDate"); got != "20261101103000" {
		t.Errorf("vnp_CreateDate = %q", got)
	}
	if query.Get("vnp_SecureHash") == "" {
		t.Fatal("missing signature")
	}

	// The signature must verify round-trip through Verify.
	result, err := client.Verify(query)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if result.TxnRef != "LW1" || result.AmountVND != 99000 {
		t.Errorf("result = %+v", result)
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	client := testClient()

	signed, err := client.BuildURL(PaymentRequest{
		TxnRef:    "LW2",
		AmountVND: 10000,
		OrderInfo: "Sach",
		ReturnURL: "https://example.com/return",
		ClientIP:  "203.0.113.7",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	parsed, _ := url.Parse(signed)
	query := parsed.Query()

	// Someone changes the amount after signing.
	tampered := cloneValues(query)
	tampered.Set("vnp_Amount", "100")
	if _, err := client.Verify(tampered); !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("tampered amount: err = %v, want ErrInvalidSignature", err)
	}

	// A different secret must not validate.
	other := New(Config{TmnCode: "TESTCODE", HashSecret: "OTHER", PaymentURL: "x"})
	if _, err := other.Verify(query); !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("wrong secret: err = %v, want ErrInvalidSignature", err)
	}

	// Missing signature.
	stripped := cloneValues(query)
	stripped.Del("vnp_SecureHash")
	if _, err := client.Verify(stripped); !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("missing signature: err = %v", err)
	}
}

func TestVerifyRejectsForeignMerchant(t *testing.T) {
	client := testClient()

	signed, err := client.BuildURL(PaymentRequest{
		TxnRef:    "LW3",
		AmountVND: 5000,
		OrderInfo: "Sach",
		ReturnURL: "https://example.com/return",
		ClientIP:  "203.0.113.7",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	parsed, _ := url.Parse(signed)
	query := cloneValues(parsed.Query())
	query.Set("vnp_TmnCode", "OTHERCODE")

	// Re-sign with our secret so only the merchant code differs.
	query.Set("vnp_SecureHash", client.sign(query))
	if _, err := client.Verify(query); !errors.Is(err, ErrWrongMerchant) {
		t.Errorf("err = %v, want ErrWrongMerchant", err)
	}
}

func TestVerifyParsesStatus(t *testing.T) {
	client := testClient()

	base := url.Values{}
	base.Set("vnp_TmnCode", "TESTCODE")
	base.Set("vnp_TxnRef", "LW4")
	base.Set("vnp_Amount", "1500000")
	base.Set("vnp_ResponseCode", SuccessCode)
	base.Set("vnp_TransactionStatus", SuccessCode)
	base.Set("vnp_TransactionNo", "14123456")
	base.Set("vnp_PayDate", "20261101103000")
	base.Set("vnp_SecureHash", client.sign(base))

	result, err := client.Verify(base)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !result.Paid {
		t.Error("Paid = false for a 00/00 callback")
	}
	if result.AmountVND != 15000 {
		t.Errorf("amount = %d, want 15000", result.AmountVND)
	}
	if result.TransactionTime.IsZero() {
		t.Error("pay date not parsed")
	}

	// A cancelled payment (24) is verified but not paid.
	cancelled := cloneValues(base)
	cancelled.Set("vnp_ResponseCode", "24")
	cancelled.Set("vnp_TransactionStatus", "02")
	cancelled.Set("vnp_SecureHash", client.sign(cancelled))
	result, err = client.Verify(cancelled)
	if err != nil {
		t.Fatalf("verify cancelled: %v", err)
	}
	if result.Paid {
		t.Error("Paid = true for a cancelled payment")
	}
}

func TestBuildURLRequiresConfig(t *testing.T) {
	unconfigured := New(Config{})
	if _, err := unconfigured.BuildURL(PaymentRequest{TxnRef: "x", AmountVND: 1000}); err == nil {
		t.Error("unconfigured client built a URL")
	}

	client := testClient()
	if _, err := client.BuildURL(PaymentRequest{TxnRef: "x", AmountVND: 0}); err == nil {
		t.Error("zero amount was accepted")
	}
}

// Signed string must be sorted by key and URL-encoded, which is what VNPay recomputes.
func TestCanonicalStringOrderAndEncoding(t *testing.T) {
	params := url.Values{}
	params.Set("b_key", "two words")
	params.Set("a_key", "1")
	params.Set("empty", "")

	got := canonicalString(params)
	want := "a_key=1&b_key=two+words"
	if got != want {
		t.Errorf("canonical = %q, want %q", got, want)
	}
	if strings.Contains(got, "empty") {
		t.Error("empty values must be skipped")
	}
}

func cloneValues(values url.Values) url.Values {
	out := url.Values{}
	for key, list := range values {
		out[key] = append([]string(nil), list...)
	}
	return out
}

// Known answer: this digest was produced with openssl from the canonical string, so it
// fails if our canonicalisation or key handling drifts from the VNPay specification.
func TestSignatureMatchesKnownAnswer(t *testing.T) {
	client := testClient()

	params := url.Values{}
	params.Set("vnp_Amount", "1000000")
	params.Set("vnp_TmnCode", "TESTCODE")
	params.Set("vnp_TxnRef", "LW1")

	const want = "8af699cfa126ec60f0711b387392cc47f05e254279b9f3ad0a719352dcbeacc2fcec0b41ef2615552d4ef4867b3cdd59a67c16cd5c483b86989622021a56182a"
	if got := client.SignValues(params); got != want {
		t.Fatalf("signature = %s, want the openssl-derived digest", got)
	}
}

// An unconfigured client must not verify anything: an empty secret would accept a
// signature the caller computed with the same empty secret.
func TestUnconfiguredClientFailsClosed(t *testing.T) {
	client := New(Config{})

	forged := url.Values{}
	forged.Set("vnp_TxnRef", "LW1")
	forged.Set("vnp_Amount", "10000")
	forged.Set("vnp_SecureHash", "deadbeef")

	if _, err := client.Verify(forged); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("err = %v, want ErrNotConfigured", err)
	}
}

// The merchant code is mandatory, not just checked when present.
func TestVerifyRequiresMerchantCode(t *testing.T) {
	client := testClient()

	params := url.Values{}
	params.Set("vnp_TxnRef", "LW1")
	params.Set("vnp_Amount", "10000")
	params.Set("vnp_SecureHash", client.SignValues(params))

	if _, err := client.Verify(params); !errors.Is(err, ErrWrongMerchant) {
		t.Errorf("err = %v, want ErrWrongMerchant", err)
	}
}

// VNPay sends amount * 100; a value with a remainder must be refused rather than
// silently truncated.
func TestVerifyRejectsFractionalAmount(t *testing.T) {
	client := testClient()

	params := url.Values{}
	params.Set("vnp_TmnCode", "TESTCODE")
	params.Set("vnp_TxnRef", "LW1")
	params.Set("vnp_Amount", "500099")
	params.Set("vnp_SecureHash", client.SignValues(params))

	if _, err := client.Verify(params); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("err = %v, want ErrInvalidAmount", err)
	}

	params.Set("vnp_Amount", "0")
	params.Set("vnp_SecureHash", client.SignValues(params))
	if _, err := client.Verify(params); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("zero amount: err = %v", err)
	}
}

func TestBuildURLRejectsOverflowingAmount(t *testing.T) {
	client := testClient()

	// MaxInt64/100 + 1 would overflow once multiplied by 100.
	if _, err := client.BuildURL(PaymentRequest{TxnRef: "x", AmountVND: 1<<62 + 1}); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("err = %v, want ErrInvalidAmount", err)
	}
}

// VNPay requires vnp_OrderInfo to be ASCII without special characters; a Vietnamese
// title must be transliterated instead of rejected at payment time.
func TestSanitizeTextForOrderInfo(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Động vật quanh bé", "dong vat quanh be"},
		{"Gói: Kỹ năng sống (3-6 tuổi)!", "goi ky nang song 3-6 tuoi"},
		{"Sách 100% hay?", "sach 100 hay"},
	} {
		if got := SanitizeText(tc.in); got != tc.want {
			t.Errorf("SanitizeText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildURLRejectsAmountAboveGatewayLimit(t *testing.T) {
	client := testClient()

	// 10 billion VND * 100 is 13 digits, beyond VNPay's numeric[1,12].
	if _, err := client.BuildURL(PaymentRequest{TxnRef: "x", AmountVND: 10_000_000_000}); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("err = %v, want ErrInvalidAmount", err)
	}
	if _, err := client.BuildURL(PaymentRequest{TxnRef: "x", AmountVND: 9_999_999_999}); err != nil {
		t.Errorf("largest allowed amount rejected: %v", err)
	}
}
