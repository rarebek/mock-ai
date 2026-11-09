// Package payment chooses the payment provider from configuration.
//
// To add a provider, implement application.Provider in its own package (see
// the checklist on that interface) and add a case below. Its credentials come
// from the environment, read in that provider's constructor.
package payment

import (
	"fmt"
	"strings"

	app "github.com/own/mock-ai/apps/api/internal/stars/application"
)

// New returns the provider named by PAYMENT_PROVIDER. Empty or "none" keeps
// sales closed: the stars page shows packages and "coming soon".
func New(name string) (app.Provider, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "none":
		return app.Unavailable{}, nil
	// case "payme":
	// 	return payme.New(os.Getenv("PAYME_MERCHANT_ID"), os.Getenv("PAYME_KEY"))
	// case "click":
	// 	return click.New(os.Getenv("CLICK_SERVICE_ID"), os.Getenv("CLICK_MERCHANT_ID"), os.Getenv("CLICK_SECRET_KEY"))
	default:
		return nil, fmt.Errorf("unknown PAYMENT_PROVIDER %q", name)
	}
}
