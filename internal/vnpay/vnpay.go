// Package vnpay signs and verifies VNPay payment URLs (Pay API 2.1.0, HMAC-SHA512).
// Nothing here touches the database: it is pure crypto so it can be tested without a
// sandbox account.
package vnpay

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const (
	Version   = "2.1.0"
	Command   = "pay"
	CurrCode  = "VND"
	Locale    = "vn"
	OrderType = "other"

	SuccessCode = "00"

	// DateFormat is the timestamp format VNPay requires.
	DateFormat = "20060102150405"
)

var (
	ErrInvalidSignature = errors.New("invalid vnpay signature")
	ErrWrongMerchant    = errors.New("vnpay tmn code mismatch")
	ErrNotConfigured    = errors.New("vnpay is not configured")
	ErrInvalidAmount    = errors.New("invalid vnpay amount")
)

// Config is static merchant configuration.
type Config struct {
	TmnCode    string
	HashSecret string
	PaymentURL string
}

func (c Config) Configured() bool {
	return c.TmnCode != "" && c.HashSecret != "" && c.PaymentURL != ""
}

// Client builds payment URLs and verifies callbacks.
type Client struct {
	cfg Config
	now func() time.Time
}

func New(cfg Config) *Client {
	return &Client{cfg: cfg, now: time.Now}
}

// PaymentRequest is what the checkout page needs for one order.
type PaymentRequest struct {
	TxnRef      string
	AmountVND   int64
	OrderInfo   string
	ReturnURL   string
	ClientIP    string
	CreateDate  time.Time
	BankCode    string // optional: preselects a bank on the VNPay page
	ExpireAfter time.Duration
}

// BuildURL returns the signed payment URL the browser must be sent to.
func (c *Client) BuildURL(req PaymentRequest) (string, error) {
	if !c.cfg.Configured() {
		return "", ErrNotConfigured
	}
	if req.AmountVND <= 0 {
		return "", ErrInvalidAmount
	}
	// VNPay takes amount*100; refuse values that would overflow int64.
	if req.AmountVND > maxAmountVND || req.AmountVND > math.MaxInt64/100 {
		return "", ErrInvalidAmount
	}
	if len(req.OrderInfo) > maxOrderInfo {
		return "", fmt.Errorf("%w: order info is too long", ErrInvalidAmount)
	}

	created := req.CreateDate
	if created.IsZero() {
		created = c.now()
	}

	params := url.Values{}
	params.Set("vnp_Version", Version)
	params.Set("vnp_Command", Command)
	params.Set("vnp_TmnCode", c.cfg.TmnCode)
	params.Set("vnp_Locale", Locale)
	params.Set("vnp_CurrCode", CurrCode)
	params.Set("vnp_TxnRef", req.TxnRef)
	params.Set("vnp_OrderInfo", req.OrderInfo)
	params.Set("vnp_OrderType", OrderType)
	// VNPay expects the smallest currency unit: VND has no minor unit, but the API
	// still wants amount * 100.
	params.Set("vnp_Amount", strconv.FormatInt(req.AmountVND*100, 10))
	params.Set("vnp_ReturnUrl", req.ReturnURL)
	params.Set("vnp_IpAddr", req.ClientIP)
	params.Set("vnp_CreateDate", created.In(vnLocation).Format(DateFormat))
	if req.ExpireAfter > 0 {
		params.Set("vnp_ExpireDate", created.Add(req.ExpireAfter).In(vnLocation).Format(DateFormat))
	}
	if req.BankCode != "" {
		params.Set("vnp_BankCode", req.BankCode)
	}
	params.Set("vnp_SecureHash", c.sign(params))

	if strings.Contains(c.cfg.PaymentURL, "?") {
		return c.cfg.PaymentURL + "&" + params.Encode(), nil
	}
	return c.cfg.PaymentURL + "?" + params.Encode(), nil
}

// Result is a verified callback (either the browser Return or the server IPN).
type Result struct {
	TxnRef            string
	AmountRaw         int64 // as sent by VNPay: amount * 100
	AmountVND         int64
	ResponseCode      string
	TransactionStatus string
	TransactionNo     string
	TransactionTime   time.Time
	BankCode          string
	// Paid is true when the response code says success (used by the return page).
	Paid bool
	// Confirmed requires the transaction status too: only an IPN may grant access.
	Confirmed bool
}

// Verify validates the signature and merchant code, then parses the callback. It fails
// closed when the gateway is not configured: an empty hash secret would otherwise let
// anyone sign a callback.
func (c *Client) Verify(query url.Values) (Result, error) {
	if !c.cfg.Configured() {
		return Result{}, ErrNotConfigured
	}

	provided := query.Get("vnp_SecureHash")
	if provided == "" {
		return Result{}, fmt.Errorf("%w: missing vnp_SecureHash", ErrInvalidSignature)
	}

	unsigned := url.Values{}
	for key, values := range query {
		if key == "vnp_SecureHash" || key == "vnp_SecureHashType" {
			continue
		}
		unsigned[key] = values
	}

	expected := c.sign(unsigned)
	if !hmac.Equal([]byte(strings.ToLower(expected)), []byte(strings.ToLower(provided))) {
		return Result{}, ErrInvalidSignature
	}

	// The merchant code is mandatory: a callback that names no merchant is not ours.
	if query.Get("vnp_TmnCode") != c.cfg.TmnCode {
		return Result{}, ErrWrongMerchant
	}

	rawAmount, err := strconv.ParseInt(query.Get("vnp_Amount"), 10, 64)
	if err != nil || rawAmount <= 0 || rawAmount%100 != 0 {
		return Result{}, fmt.Errorf("%w: %q", ErrInvalidAmount, query.Get("vnp_Amount"))
	}

	result := Result{
		TxnRef:            query.Get("vnp_TxnRef"),
		AmountRaw:         rawAmount,
		AmountVND:         rawAmount / 100,
		ResponseCode:      query.Get("vnp_ResponseCode"),
		TransactionStatus: query.Get("vnp_TransactionStatus"),
		TransactionNo:     query.Get("vnp_TransactionNo"),
		BankCode:          query.Get("vnp_BankCode"),
	}
	if raw := query.Get("vnp_PayDate"); raw != "" {
		if parsed, err := time.ParseInLocation(DateFormat, raw, vnLocation); err == nil {
			result.TransactionTime = parsed
		}
	}

	// The browser Return only carries ResponseCode; the IPN carries both. Paid means the
	// payment succeeded (for display); only a complete IPN may grant access.
	result.Paid = result.ResponseCode == SuccessCode
	result.Confirmed = result.ResponseCode == SuccessCode && result.TransactionStatus == SuccessCode
	return result, nil
}

// sign builds the canonical string VNPay expects: url-encoded, sorted by key, joined
// with '&', then HMAC-SHA512 hex.
func (c *Client) sign(params url.Values) string {
	canonical := canonicalString(params)
	mac := hmac.New(sha512.New, []byte(c.cfg.HashSecret))
	mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}

func canonicalString(params url.Values) string {
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		// The signature fields themselves are never signed.
		if key == "vnp_SecureHash" || key == "vnp_SecureHashType" {
			continue
		}
		// Skip empty values: VNPay signs only what it sends.
		if len(params[key]) == 0 || params[key][0] == "" {
			continue
		}
		parts = append(parts, url.QueryEscape(key)+"="+url.QueryEscape(params[key][0]))
	}
	return strings.Join(parts, "&")
}

// SignValues signs an arbitrary parameter set, ignoring vnp_SecureHash/Type. It exists
// so integration tests can build a callback that the same code will verify.
func (c *Client) SignValues(params url.Values) string {
	return c.sign(params)
}

// vnLocation is the timezone VNPay formats timestamps in; containers usually run UTC.
var vnLocation = time.FixedZone("ICT", 7*60*60)

// SanitizeText makes a string acceptable for vnp_OrderInfo: VNPay requires ASCII with
// no special characters ("Tiếng Việt không dấu"), so diacritics and punctuation are
// dropped rather than rejected by the gateway at payment time.
func SanitizeText(value string) string {
	lowered := strings.ReplaceAll(strings.ToLower(value), "đ", "d")

	var b strings.Builder
	for _, r := range norm.NFD.String(lowered) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == ' ', r == '.', r == '-':
			if b.Len() < maxOrderInfo {
				b.WriteRune(r)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

const maxOrderInfo = 255

// maxAmountVND keeps vnp_Amount inside VNPay's numeric[1,12] (amount*100 must fit).
const maxAmountVND = 9_999_999_999
