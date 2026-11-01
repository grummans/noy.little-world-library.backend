// Package config loads runtime configuration from environment variables.
// Secrets must only ever arrive this way; nothing here is committed.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"littleworld/internal/storage"
	"littleworld/internal/vnpay"
)

type Config struct {
	HTTPAddr    string
	AppEnv      string
	DatabaseURL string
	S3          storage.Config
	Vnpay       vnpay.Config
	// VnpayReturnURL is where VNPay sends the browser after payment.
	VnpayReturnURL string
}

func Load() Config {
	return Config{
		HTTPAddr: getenv("HTTP_ADDR", ":8080"),
		// Defaulting to prod means a forgotten APP_ENV never yields an insecure
		// (non-Secure) session cookie in production.
		AppEnv:         getenv("APP_ENV", "prod"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		S3:             storage.ConfigFromEnv(),
		Vnpay:          vnpayConfigFromEnv(),
		VnpayReturnURL: os.Getenv("VNPAY_RETURN_URL"),
	}
}

func vnpayConfigFromEnv() vnpay.Config {
	return vnpay.Config{
		TmnCode:    os.Getenv("VNPAY_TMN_CODE"),
		HashSecret: os.Getenv("VNPAY_HASH_SECRET"),
		PaymentURL: getenv("VNPAY_URL", "https://sandbox.vnpayment.vn/paymentv2/vpcpay.html"),
	}
}

// getenv reads an environment variable, falling back when unset.
func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Validate refuses half-configured integrations. An empty VNPAY_TMN_CODE and
// VNPAY_HASH_SECRET mean "payments disabled" (checkout returns 503); setting only one of
// them, or forgetting the return URL, is a deployment mistake and fails at startup
// instead of silently signing with an empty secret.
func (c Config) Validate() error {
	if c.DatabaseURL == "" {
		return errors.New("DATABASE_URL is empty")
	}

	if c.Vnpay.TmnCode == "" && c.Vnpay.HashSecret == "" {
		return nil
	}
	missing := []string{}
	if c.Vnpay.TmnCode == "" {
		missing = append(missing, "VNPAY_TMN_CODE")
	}
	if c.Vnpay.HashSecret == "" {
		missing = append(missing, "VNPAY_HASH_SECRET")
	}
	if c.Vnpay.PaymentURL == "" {
		missing = append(missing, "VNPAY_URL")
	}
	if c.VnpayReturnURL == "" {
		missing = append(missing, "VNPAY_RETURN_URL")
	}
	if len(missing) > 0 {
		return fmt.Errorf("incomplete VNPay configuration: %s", strings.Join(missing, ", "))
	}
	return nil
}
