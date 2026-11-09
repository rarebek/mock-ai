// Package payment chooses the payment provider from configuration.
//
// To add a provider, implement application.Provider in its own package (see
// the checklist on that interface) and add a case below.
package payment

import (
	"fmt"
	"log/slog"
	"strings"

	app "github.com/own/mock-ai/apps/api/internal/stars/application"
	"github.com/own/mock-ai/apps/api/internal/stars/payment/inpay"
)

type Config struct {
	// Provider is PAYMENT_PROVIDER: "" or "none" keeps sales closed.
	Provider string
	// PublicBaseURL is where payment providers reach this API from the
	// internet (the student app's origin; /api is proxied to the backend).
	PublicBaseURL      string
	InpaySecretKey     string
	InpayWebhookSecret string
	InpayBaseURL       string
	Log                *slog.Logger
}

func New(c Config) (app.Provider, error) {
	switch strings.ToLower(strings.TrimSpace(c.Provider)) {
	case "", "none":
		return app.Unavailable{}, nil
	case "inpay":
		if !strings.HasPrefix(c.InpaySecretKey, "sk_") {
			return nil, fmt.Errorf("PAYMENT_PROVIDER=inpay needs INPAY_SECRET_KEY (sk_test_… or sk_live_…)")
		}
		callback := strings.TrimRight(c.PublicBaseURL, "/") + "/api/v1/payments/inpay/callback"
		return inpay.New(c.InpaySecretKey, c.InpayWebhookSecret, c.InpayBaseURL, callback, c.Log), nil
	default:
		return nil, fmt.Errorf("unknown PAYMENT_PROVIDER %q", c.Provider)
	}
}
