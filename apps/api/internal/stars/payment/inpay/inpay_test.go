package inpay

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	app "github.com/own/mock-ai/apps/api/internal/stars/application"
)

// fakeInpay records the create request and serves one payment's state.
type fakeInpay struct {
	created     map[string]any
	idempotency string
	auth        string
	payment     payment
}

func (f *fakeInpay) server(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/payments/":
			f.idempotency = r.Header.Get("Idempotency-Key")
			_ = json.NewDecoder(r.Body).Decode(&f.created)
			fmt.Fprintf(w, `{"success":true,"data":{"id":"47907909afd21a28","status":"requires_payment_method","checkout_url":"https://inpay.uz/checkout/47907909afd21a28"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/payments/"+f.payment.ID:
			b, _ := json.Marshal(map[string]any{"success": true, "data": f.payment})
			_, _ = w.Write(b)
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"success":false,"error":{"code":"resource_missing","message":"no such payment"}}`)
		}
	}))
}

type fakeSettler struct {
	purchase      app.Purchase
	paid, failed  int
	paidReference string
}

func (s *fakeSettler) Purchase(_ context.Context, id string) (app.Purchase, error) {
	if id != s.purchase.ID {
		return app.Purchase{}, app.ErrNotFound
	}
	return s.purchase, nil
}
func (s *fakeSettler) MarkPaid(_ context.Context, _, ref string) (app.Purchase, error) {
	s.paid++
	s.paidReference = ref
	return s.purchase, nil
}
func (s *fakeSettler) MarkFailed(context.Context, string, string) (app.Purchase, error) {
	s.failed++
	return s.purchase, nil
}

var purchase = app.Purchase{ID: "6f1c2a40-0000-4000-8000-000000000001", Stars: 5, AmountMinor: 15_000_000, Currency: "UZS", Provider: "inpay", ProviderReference: "47907909afd21a28"}

func sign(secret string, t int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", t)
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", t, hex.EncodeToString(mac.Sum(nil)))
}

func webhook(t *testing.T, p *Provider, s *fakeSettler, body []byte, signature string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments/inpay/callback", strings.NewReader(string(body)))
	if signature != "" {
		req.Header.Set("X-Inpay-Signature", signature)
	}
	rec := httptest.NewRecorder()
	p.HandleCallback(rec, req, s)
	return rec.Code
}

func succeededEvent(paymentID, purchaseID string, amount int64) []byte {
	b, _ := json.Marshal(map[string]any{
		"id": "evt_1", "object": "event", "type": "payment.succeeded",
		"data": map[string]any{"object": map[string]any{"id": paymentID, "amount": amount, "currency": "UZS", "status": "succeeded",
			"metadata": map[string]string{"purchase_id": purchaseID}}},
	})
	return b
}

func TestCheckoutCreatesAPaymentInWholeSom(t *testing.T) {
	f := &fakeInpay{}
	srv := f.server(t)
	defer srv.Close()
	p := New("sk_test_abc", "", srv.URL, "https://mock-ai.uz/api/v1/payments/inpay/callback", nil)
	out, err := p.Checkout(context.Background(), app.CheckoutRequest{Purchase: purchase, ReturnURL: "https://mock-ai.uz/?purchase=x#stars"})
	if err != nil || out.URL != "https://inpay.uz/checkout/47907909afd21a28" || out.Reference != "47907909afd21a28" {
		t.Fatalf("checkout: %+v %v", out, err)
	}
	if f.auth != "Bearer sk_test_abc" || f.idempotency != "mockai-"+purchase.ID {
		t.Fatalf("headers: auth=%q idempotency=%q", f.auth, f.idempotency)
	}
	if f.created["amount"] != float64(150000) || f.created["currency"] != "UZS" ||
		f.created["return_url"] != "https://mock-ai.uz/?purchase=x#stars" ||
		f.created["webhook_url"] != "https://mock-ai.uz/api/v1/payments/inpay/callback" {
		t.Fatalf("create body: %+v", f.created)
	}
	if md, _ := f.created["metadata"].(map[string]any); md["purchase_id"] != purchase.ID {
		t.Fatalf("metadata: %+v", f.created["metadata"])
	}
}

func TestCheckoutRefusesFractionalOrTinyAmounts(t *testing.T) {
	p := New("sk_test_abc", "", "http://unused", "", nil)
	for _, amount := range []int64{99_900, 150_050} {
		pur := purchase
		pur.AmountMinor = amount
		if _, err := p.Checkout(context.Background(), app.CheckoutRequest{Purchase: pur}); err == nil {
			t.Fatalf("amount %d accepted", amount)
		}
	}
}

func TestSignedPaidWebhookCreditsAfterConfirmingWithInpay(t *testing.T) {
	f := &fakeInpay{payment: payment{ID: "47907909afd21a28", Amount: 150000, Currency: "UZS", Status: "succeeded", Metadata: map[string]string{"purchase_id": purchase.ID}}}
	srv := f.server(t)
	defer srv.Close()
	p := New("sk_live_abc", "whsec_test", srv.URL, "", nil)
	s := &fakeSettler{purchase: purchase}
	body := succeededEvent("47907909afd21a28", purchase.ID, 150000)
	if code := webhook(t, p, s, body, sign("whsec_test", time.Now().Unix(), body)); code != 200 || s.paid != 1 || s.paidReference != "47907909afd21a28" {
		t.Fatalf("code %d paid %d ref %q", code, s.paid, s.paidReference)
	}
}

func TestForgedWebhookIsNotTrustedOverInpaysRecord(t *testing.T) {
	// No webhook secret configured: the body claims success, but inPAY says
	// the payment is still waiting, so nothing is credited.
	f := &fakeInpay{payment: payment{ID: "47907909afd21a28", Amount: 150000, Currency: "UZS", Status: "requires_payment_method", Metadata: map[string]string{"purchase_id": purchase.ID}}}
	srv := f.server(t)
	defer srv.Close()
	p := New("sk_test_abc", "", srv.URL, "", nil)
	s := &fakeSettler{purchase: purchase}
	if code := webhook(t, p, s, succeededEvent("47907909afd21a28", purchase.ID, 150000), ""); code != 200 || s.paid != 0 {
		t.Fatalf("code %d paid %d", code, s.paid)
	}
}

func TestBadOrStaleSignatureIsRejected(t *testing.T) {
	p := New("sk_live_abc", "whsec_test", "http://unused", "", nil)
	s := &fakeSettler{purchase: purchase}
	body := succeededEvent("47907909afd21a28", purchase.ID, 150000)
	for name, sig := range map[string]string{
		"missing": "",
		"wrong":   sign("whsec_other", time.Now().Unix(), body),
		"stale":   sign("whsec_test", time.Now().Add(-10*time.Minute).Unix(), body),
	} {
		if code := webhook(t, p, s, body, sig); code != 400 || s.paid != 0 {
			t.Fatalf("%s signature: code %d paid %d", name, code, s.paid)
		}
	}
}

func TestAmountMismatchIsNeverCredited(t *testing.T) {
	f := &fakeInpay{payment: payment{ID: "47907909afd21a28", Amount: 1000, Currency: "UZS", Status: "succeeded", Metadata: map[string]string{"purchase_id": purchase.ID}}}
	srv := f.server(t)
	defer srv.Close()
	p := New("sk_test_abc", "", srv.URL, "", nil)
	if state, err := p.Status(context.Background(), purchase); err == nil || state == app.StatePaid {
		t.Fatalf("underpaid payment: state %q err %v", state, err)
	}
	s := &fakeSettler{purchase: purchase}
	if code := webhook(t, p, s, succeededEvent("47907909afd21a28", purchase.ID, 1000), ""); code != 200 || s.paid != 0 {
		t.Fatalf("code %d paid %d", code, s.paid)
	}
}

func TestStatusMapsInpayStates(t *testing.T) {
	cases := map[string]app.PaymentState{"succeeded": app.StatePaid, "failed": app.StateFailed, "canceled": app.StateFailed,
		"processing": app.StatePending, "requires_action": app.StatePending, "requires_payment_method": app.StatePending}
	for status, want := range cases {
		got, err := stateFor(payment{ID: "x", Amount: 150000, Currency: "UZS", Status: status, Metadata: map[string]string{"purchase_id": purchase.ID}}, purchase)
		if err != nil || got != want {
			t.Fatalf("%s: got %q err %v, want %q", status, got, err, want)
		}
	}
	if _, err := stateFor(payment{ID: "x", Status: "succeeded", Metadata: map[string]string{"purchase_id": "someone-else"}}, purchase); err == nil {
		t.Fatal("payment for another purchase accepted")
	}
}

func TestUnrelatedEventsAreAcknowledged(t *testing.T) {
	p := New("sk_test_abc", "", "http://unused", "", nil)
	s := &fakeSettler{purchase: purchase}
	body := []byte(`{"id":"evt_2","type":"subscription.created","data":{"object":{"id":"sub_1"}}}`)
	if code := webhook(t, p, s, body, ""); code != 200 || s.paid != 0 {
		t.Fatalf("code %d paid %d", code, s.paid)
	}
}

func TestDescriptionNamesWhatIsBought(t *testing.T) {
	for _, c := range []struct {
		kind app.Kind
		n    int32
		want string
	}{
		{app.KindStar, 1, "Mock AI: 1 star (one full IELTS mock)"},
		{app.KindStar, 5, "Mock AI: 5 stars (5 full IELTS mocks)"},
		{app.KindWriting, 1, "Mock AI: 1 Writing check (one AI-evaluated essay)"},
		{app.KindSpeaking, 5, "Mock AI: 5 Speaking interviews (5 AI-assessed interviews)"},
	} {
		if got := description(c.kind, c.n); got != c.want {
			t.Errorf("description(%s, %d) = %q, want %q", c.kind, c.n, got, c.want)
		}
	}
}
