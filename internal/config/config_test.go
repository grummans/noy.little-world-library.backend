package config

import (
	"strings"
	"testing"

	"littleworld/internal/vnpay"
)

func base() Config {
	return Config{DatabaseURL: "postgres://x", Vnpay: vnpay.Config{PaymentURL: "https://sandbox"}}
}

func TestValidateRequiresDatabaseURL(t *testing.T) {
	cfg := base()
	cfg.DatabaseURL = ""
	if err := cfg.Validate(); err == nil {
		t.Error("empty DATABASE_URL accepted")
	}
}

// No VNPay credentials means checkout is disabled, not broken.
func TestValidateAllowsDisabledPayments(t *testing.T) {
	cfg := base()
	if err := cfg.Validate(); err != nil {
		t.Errorf("disabled payments rejected: %v", err)
	}
}

func TestValidateRejectsHalfConfiguredVnpay(t *testing.T) {
	cfg := base()
	cfg.Vnpay.TmnCode = "ABC"
	if err := cfg.Validate(); err == nil {
		t.Fatal("merchant code without secret accepted")
	} else if !strings.Contains(err.Error(), "VNPAY_HASH_SECRET") {
		t.Errorf("err = %v, want it to name the missing secret", err)
	}

	cfg.Vnpay.HashSecret = "SECRET"
	cfg.VnpayReturnURL = "https://example.com/return"
	if err := cfg.Validate(); err != nil {
		t.Errorf("complete configuration rejected: %v", err)
	}
}

func TestValidateRequiresReturnURLWhenPaymentsOn(t *testing.T) {
	cfg := base()
	cfg.Vnpay.TmnCode = "ABC"
	cfg.Vnpay.HashSecret = "SECRET"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "VNPAY_RETURN_URL") {
		t.Errorf("err = %v, want it to name VNPAY_RETURN_URL", err)
	}
}
