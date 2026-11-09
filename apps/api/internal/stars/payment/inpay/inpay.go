// Package inpay sells stars through inPAY (https://inpay.uz/api/v2/).
//
// Flow:
//
//  1. Checkout creates a payment (POST /payments/) for the purchase's amount
//     in whole so'm, with our purchase id in metadata and as the
//     Idempotency-Key, and returns inPAY's hosted checkout_url.
//  2. inPAY calls our webhook (POST /api/v1/payments/inpay/callback). If a
//     webhook secret is configured the X-Inpay-Signature is verified. Either
//     way the payment is then fetched from inPAY with our own secret key
//     (GET /payments/{id}) and only that answer is trusted, so a forged or
//     replayed notification cannot credit stars.
//  3. Status lets the service settle a purchase the webhook has not reached
//     yet, when the buyer lands back on the return page.
package inpay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	app "github.com/own/mock-ai/apps/api/internal/stars/application"
)

const DefaultBaseURL = "https://inpay.uz/api/v2"

type Provider struct {
	secretKey     string
	webhookSecret string
	baseURL       string
	callbackURL   string
	client        *http.Client
	log           *slog.Logger
	now           func() time.Time
}

// New configures the provider. callbackURL is the public address inPAY
// should notify, e.g. https://mock-ai.uz/api/v1/payments/inpay/callback.
// webhookSecret (whsec_…) is optional; without it every notification is still
// confirmed against the API before stars are credited.
func New(secretKey, webhookSecret, baseURL, callbackURL string, log *slog.Logger) *Provider {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if log == nil {
		log = slog.Default()
	}
	return &Provider{
		secretKey: strings.TrimSpace(secretKey), webhookSecret: strings.TrimSpace(webhookSecret),
		baseURL: strings.TrimRight(baseURL, "/"), callbackURL: callbackURL,
		client: &http.Client{Timeout: 20 * time.Second}, log: log, now: time.Now,
	}
}

func (p *Provider) Name() string    { return "inpay" }
func (p *Provider) Available() bool { return p.secretKey != "" }

// Live reports whether this is a live (sk_live_) key rather than a test key.
func (p *Provider) Live() bool { return strings.HasPrefix(p.secretKey, "sk_live_") }

type payment struct {
	ID          string            `json:"id"`
	Amount      int64             `json:"amount"`
	Currency    string            `json:"currency"`
	Status      string            `json:"status"`
	CheckoutURL string            `json:"checkout_url"`
	Metadata    map[string]string `json:"metadata"`
}

type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// APIError is an error inPAY returned, kept so operators can see the code.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("inpay %d %s: %s", e.Status, e.Code, e.Message) }

func (p *Provider) do(ctx context.Context, method, path string, body any, idempotencyKey string, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.secretKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	res, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("inpay %d: unreadable response", res.StatusCode)
	}
	if res.StatusCode >= 300 || !env.Success {
		e := &APIError{Status: res.StatusCode}
		if env.Error != nil {
			e.Code, e.Message = env.Error.Code, env.Error.Message
		}
		return e
	}
	return json.Unmarshal(env.Data, out)
}

func (p *Provider) Checkout(ctx context.Context, req app.CheckoutRequest) (app.Checkout, error) {
	pur := req.Purchase
	if pur.Currency != "UZS" || pur.AmountMinor%100 != 0 || pur.AmountMinor < 1000_00 {
		return app.Checkout{}, fmt.Errorf("inpay needs a whole UZS amount of at least 1 000, got %d %s", pur.AmountMinor, pur.Currency)
	}
	body := map[string]any{
		"amount":      pur.AmountMinor / 100,
		"currency":    "UZS",
		"description": description(pur.Stars),
		"return_url":  req.ReturnURL,
		"metadata":    map[string]string{"purchase_id": pur.ID, "stars": strconv.Itoa(int(pur.Stars))},
	}
	if p.callbackURL != "" {
		body["webhook_url"] = p.callbackURL
	}
	var out payment
	if err := p.do(ctx, http.MethodPost, "/payments/", body, "mockai-"+pur.ID, &out); err != nil {
		p.log.Error("inpay checkout", "purchase", pur.ID, "error", err)
		return app.Checkout{}, err
	}
	if out.CheckoutURL == "" || out.ID == "" {
		return app.Checkout{}, errors.New("inpay returned no checkout url")
	}
	return app.Checkout{URL: out.CheckoutURL, Reference: out.ID}, nil
}

func description(stars int32) string {
	if stars == 1 {
		return "Mock AI: 1 star (one full IELTS mock)"
	}
	return fmt.Sprintf("Mock AI: %d stars (%d full IELTS mocks)", stars, stars)
}

func (p *Provider) fetch(ctx context.Context, paymentID string) (payment, error) {
	var out payment
	if !paymentIDPattern.MatchString(paymentID) {
		return out, fmt.Errorf("invalid inpay payment id %q", paymentID)
	}
	err := p.do(ctx, http.MethodGet, "/payments/"+paymentID, nil, "", &out)
	return out, err
}

var paymentIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{6,64}$`)

// Status asks inPAY for the payment behind a purchase and checks that it is
// the payment we created: same purchase id in metadata, same amount.
func (p *Provider) Status(ctx context.Context, pur app.Purchase) (app.PaymentState, error) {
	pay, err := p.fetch(ctx, pur.ProviderReference)
	if err != nil {
		return app.StatePending, err
	}
	return stateFor(pay, pur)
}

func stateFor(pay payment, pur app.Purchase) (app.PaymentState, error) {
	if pay.Metadata["purchase_id"] != pur.ID {
		return app.StatePending, fmt.Errorf("inpay payment %s belongs to purchase %q, not %s", pay.ID, pay.Metadata["purchase_id"], pur.ID)
	}
	switch pay.Status {
	case "succeeded":
		if pay.Amount*100 != pur.AmountMinor || !strings.EqualFold(pay.Currency, pur.Currency) {
			return app.StatePending, fmt.Errorf("inpay payment %s paid %d %s, expected %d minor %s", pay.ID, pay.Amount, pay.Currency, pur.AmountMinor, pur.Currency)
		}
		return app.StatePaid, nil
	case "failed", "canceled":
		return app.StateFailed, nil
	default: // requires_payment_method, requires_action, processing
		return app.StatePending, nil
	}
}

type event struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object payment `json:"object"`
	} `json:"data"`
}

var signaturePattern = regexp.MustCompile(`t=(\d+),v1=([a-f0-9]+)`)

// verifySignature checks X-Inpay-Signature: HMAC-SHA256 over "{t}.{raw body}"
// with the webhook secret, at most five minutes old.
func (p *Provider) verifySignature(header string, body []byte) error {
	m := signaturePattern.FindStringSubmatch(header)
	if m == nil {
		return errors.New("missing or malformed signature")
	}
	t, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return errors.New("bad signature timestamp")
	}
	if age := p.now().Sub(time.Unix(t, 0)); age > 5*time.Minute || age < -5*time.Minute {
		return errors.New("signature timestamp outside the five-minute window")
	}
	mac := hmac.New(sha256.New, []byte(p.webhookSecret))
	mac.Write([]byte(m[1] + "."))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(m[2])) {
		return errors.New("signature mismatch")
	}
	return nil
}

func (p *Provider) HandleCallback(w http.ResponseWriter, r *http.Request, settle app.Settler) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}
	if p.webhookSecret != "" {
		if err := p.verifySignature(r.Header.Get("X-Inpay-Signature"), body); err != nil {
			p.log.Warn("inpay webhook rejected", "error", err)
			http.Error(w, "invalid signature", http.StatusBadRequest)
			return
		}
	}
	var ev event
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	purchaseID := ev.Data.Object.Metadata["purchase_id"]
	if !strings.HasPrefix(ev.Type, "payment.") || purchaseID == "" {
		received(w) // not one of ours (e.g. subscriptions); acknowledge so it is not retried
		return
	}
	ctx := r.Context()
	pur, err := settle.Purchase(ctx, purchaseID)
	if errors.Is(err, app.ErrNotFound) {
		p.log.Warn("inpay webhook for unknown purchase", "purchase", purchaseID, "event", ev.ID)
		received(w)
		return
	}
	if err != nil {
		http.Error(w, "try again", http.StatusInternalServerError)
		return
	}
	if pur.Provider != p.Name() || (pur.ProviderReference != "" && pur.ProviderReference != ev.Data.Object.ID) {
		p.log.Warn("inpay webhook does not match purchase", "purchase", purchaseID, "payment", ev.Data.Object.ID)
		received(w)
		return
	}
	// Trust inPAY's own record, fetched with our key, not the notification body.
	pay, err := p.fetch(ctx, ev.Data.Object.ID)
	if err != nil {
		p.log.Error("inpay webhook confirmation", "purchase", purchaseID, "error", err)
		http.Error(w, "try again", http.StatusBadGateway) // inPAY retries
		return
	}
	state, err := stateFor(pay, pur)
	if err != nil {
		p.log.Error("inpay webhook mismatch", "purchase", purchaseID, "error", err)
		received(w)
		return
	}
	switch state {
	case app.StatePaid:
		_, err = settle.MarkPaid(ctx, pur.ID, pay.ID)
	case app.StateFailed:
		_, err = settle.MarkFailed(ctx, pur.ID, "inpay: "+pay.Status)
	}
	if err != nil {
		http.Error(w, "try again", http.StatusInternalServerError)
		return
	}
	received(w)
}

func received(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"received":true}`))
}
