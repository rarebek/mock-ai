package application

import (
	"context"
	"net/http"
)

// Provider is the seam for a payment company. To add one (Payme, Click,
// Uzum, Stripe …):
//
//  1. Create a package under internal/stars/payment/<name> with a type that
//     implements Provider.
//  2. Checkout: build the provider's payment page URL (or call its API) for
//     req.Purchase and return it. Return the provider's own transaction id
//     as Reference if it gives one up front.
//  3. HandleCallback: verify the provider's server-to-server notification
//     (signature, merchant id, amount equals Purchase.AmountMinor), then call
//     settle.MarkPaid or settle.MarkFailed. Write whatever response body the
//     provider's protocol expects. Stars are credited by MarkPaid, exactly
//     once, even if the provider retries the notification.
//  4. Register it in internal/stars/payment.New and set PAYMENT_PROVIDER plus
//     its credentials in the environment.
//
// The callback is served at POST /api/v1/payments/<name>/callback.
type Provider interface {
	Name() string
	// Available reports whether students can buy stars right now.
	Available() bool
	Checkout(ctx context.Context, req CheckoutRequest) (Checkout, error)
	HandleCallback(w http.ResponseWriter, r *http.Request, settle Settler)
}

type CheckoutRequest struct {
	Purchase Purchase
	// ReturnURL is where the provider should send the student afterwards.
	// It carries ?purchase=<id> so the app can show the outcome.
	ReturnURL string
}

type Checkout struct {
	URL       string
	Reference string
}

// StatusChecker is implemented by providers that can be asked for a
// payment's state directly. The service uses it to settle a purchase the
// callback has not settled yet, and providers use it to confirm a callback.
type StatusChecker interface {
	Status(ctx context.Context, p Purchase) (PaymentState, error)
}

type PaymentState string

const (
	StatePending PaymentState = "pending"
	StatePaid    PaymentState = "paid"
	StateFailed  PaymentState = "failed"
)

// Settler is the only way a Provider can change a purchase.
type Settler interface {
	Purchase(ctx context.Context, purchaseID string) (Purchase, error)
	MarkPaid(ctx context.Context, purchaseID, providerReference string) (Purchase, error)
	MarkFailed(ctx context.Context, purchaseID, reason string) (Purchase, error)
}

// Unavailable is the provider used until a real one is configured: the stars
// page shows its packages, and buying explains that payments are coming soon.
type Unavailable struct{}

func (Unavailable) Name() string    { return "none" }
func (Unavailable) Available() bool { return false }
func (Unavailable) Checkout(context.Context, CheckoutRequest) (Checkout, error) {
	return Checkout{}, ErrPaymentsOff
}
func (Unavailable) HandleCallback(w http.ResponseWriter, _ *http.Request, _ Settler) {
	http.NotFound(w, nil)
}
