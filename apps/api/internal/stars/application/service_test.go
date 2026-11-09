package application

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

type fakeRepo struct {
	centre        *Centre
	centreBalance int32
	granted       map[string]bool
	price         int64
	balance       int32
	spent         map[string]bool
	purchases     map[string]Purchase
	failed        []string
	ref           string
}

func newFakeRepo(balance int32) *fakeRepo {
	return &fakeRepo{balance: balance, spent: map[string]bool{}, purchases: map[string]Purchase{}, granted: map[string]bool{}, price: 3_000_000}
}
func (f *fakeRepo) EnsureWallet(context.Context, string, int32) (int32, error) { return f.balance, nil }
func (f *fakeRepo) Spend(_ context.Context, _, reason, ref string, cost int32) (int32, error) {
	if f.spent[reason+ref] {
		return f.balance, nil
	}
	if f.balance < cost {
		return f.balance, ErrInsufficient
	}
	f.balance -= cost
	f.spent[reason+ref] = true
	return f.balance, nil
}
func (f *fakeRepo) CreatePurchase(_ context.Context, p Purchase) (Purchase, error) {
	p.ID, p.Status = "11111111-1111-1111-1111-111111111111", "pending"
	f.purchases[p.ID] = p
	return p, nil
}
func (f *fakeRepo) SetProviderReference(_ context.Context, _, ref string) error {
	f.ref = ref
	return nil
}
func (f *fakeRepo) Purchase(_ context.Context, id string) (Purchase, error) {
	p, ok := f.purchases[id]
	if !ok {
		return Purchase{}, ErrNotFound
	}
	return p, nil
}
func (f *fakeRepo) SettlePaid(context.Context, string, string) (Purchase, error) {
	return Purchase{}, nil
}
func (f *fakeRepo) SettleFailed(_ context.Context, id, _ string) (Purchase, error) {
	f.failed = append(f.failed, id)
	return Purchase{}, nil
}

func (f *fakeRepo) StudentCentre(context.Context, string) (Centre, bool, error) {
	if f.centre == nil {
		return Centre{}, false, nil
	}
	return *f.centre, true, nil
}
func (f *fakeRepo) EnsureCentreWallet(context.Context, string) (int32, error) {
	return f.centreBalance, nil
}
func (f *fakeRepo) SpendFromCentre(_ context.Context, _, user, reason, ref string, cost int32) (int32, error) {
	if f.spent[user+reason+ref] {
		return f.centreBalance, nil
	}
	if f.centreBalance < cost {
		return f.centreBalance, ErrCentreInsufficient
	}
	f.centreBalance -= cost
	f.spent[user+reason+ref] = true
	return f.centreBalance, nil
}
func (f *fakeRepo) GrantCentre(_ context.Context, _, _ string, stars int32, ref, _ string) (int32, error) {
	if !f.granted[ref] {
		f.granted[ref] = true
		f.centreBalance += stars
	}
	return f.centreBalance, nil
}
func (f *fakeRepo) CentreActivity(context.Context, string, int) ([]LedgerEntry, error) {
	return []LedgerEntry{}, nil
}
func (f *fakeRepo) Pricing(context.Context) (Pricing, error) {
	return Pricing{PricePerStarMinor: f.price, Currency: "UZS"}, nil
}
func (f *fakeRepo) SetPricing(_ context.Context, price int64, _ string) (Pricing, error) {
	f.price = price
	return f.Pricing(context.Background())
}
func (f *fakeRepo) RecentPurchases(context.Context, int) ([]PurchaseRow, error) {
	return []PurchaseRow{}, nil
}
func (f *fakeRepo) PendingPurchases(context.Context, int) ([]Purchase, error) {
	out := []Purchase{}
	for _, p := range f.purchases {
		if p.Status == "pending" {
			out = append(out, p)
		}
	}
	return out, nil
}

type fakeProvider struct {
	err      error
	lastURL  string
	lastAmnt int64
}

func (p *fakeProvider) Name() string    { return "fake" }
func (p *fakeProvider) Available() bool { return true }
func (p *fakeProvider) Checkout(_ context.Context, r CheckoutRequest) (Checkout, error) {
	p.lastURL, p.lastAmnt = r.ReturnURL, r.Purchase.AmountMinor
	if p.err != nil {
		return Checkout{}, p.err
	}
	return Checkout{URL: "https://pay.example/checkout", Reference: "prov-1"}, nil
}
func (p *fakeProvider) HandleCallback(http.ResponseWriter, *http.Request, Settler) {}

func TestSpendRules(t *testing.T) {
	s := New(newFakeRepo(1), nil, 1, "https://app", "https://panel")
	ctx := context.Background()
	if _, err := s.Spend(ctx, "u", "lottery", "ref-12345678"); !errors.Is(err, ErrUnknownPurpose) {
		t.Fatalf("unknown purpose: %v", err)
	}
	if _, err := s.Spend(ctx, "u", ReasonFullMock, "short"); !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("bad reference: %v", err)
	}
	if w, err := s.Spend(ctx, "u", ReasonFullMock, "ref-12345678"); err != nil || w.Balance != 0 {
		t.Fatalf("spend: %+v %v", w, err)
	}
	if _, err := s.Spend(ctx, "u", ReasonFullMock, "ref-87654321"); !errors.Is(err, ErrInsufficient) {
		t.Fatalf("second mock with no stars: %v", err)
	}
}

func TestOverviewWhilePaymentsAreOff(t *testing.T) {
	s := New(newFakeRepo(1), nil, 1, "https://app", "https://panel")
	o, err := s.Overview(context.Background(), "u")
	if err != nil || o.Balance != 1 || o.FullMockCost != 1 || o.PaymentsAvailable || len(o.Packages) == 0 {
		t.Fatalf("overview: %+v %v", o, err)
	}
	if _, err := s.StartPurchase(context.Background(), "u", 5); !errors.Is(err, ErrPaymentsOff) {
		t.Fatalf("purchase while off: %v", err)
	}
}

func TestUnpricedPackageIsNotSoldEvenWithAProvider(t *testing.T) {
	repo := newFakeRepo(0)
	repo.price = 0
	s := New(repo, &fakeProvider{}, 1, "https://app", "https://panel")
	if _, err := s.StartPurchase(context.Background(), "u", 5); !errors.Is(err, ErrPaymentsOff) {
		t.Fatalf("unpriced package: %v", err)
	}
}

func TestStartPurchaseHandsTheProviderAReturnURL(t *testing.T) {
	repo, prov := newFakeRepo(0), &fakeProvider{}
	s := New(repo, prov, 1, "https://mock-ai.uz", "https://panel")
	out, err := s.StartPurchase(context.Background(), "u", 5)
	if err != nil || out.CheckoutURL != "https://pay.example/checkout" {
		t.Fatalf("checkout: %+v %v", out, err)
	}
	if !strings.HasPrefix(prov.lastURL, "https://mock-ai.uz/?purchase="+out.Purchase.ID) || prov.lastAmnt != 15_000_000 {
		t.Fatalf("provider got url %q amount %d", prov.lastURL, prov.lastAmnt)
	}
	if repo.ref != "prov-1" {
		t.Fatalf("provider reference not stored: %q", repo.ref)
	}
	for _, bad := range []int32{0, -1, MaxPersonalStars + 1} {
		if _, err := s.StartPurchase(context.Background(), "u", bad); !errors.Is(err, ErrInvalidQuantity) {
			t.Fatalf("buying %d stars: %v", bad, err)
		}
	}
}

func TestFailedCheckoutMarksThePurchaseFailed(t *testing.T) {
	repo := newFakeRepo(0)
	s := New(repo, &fakeProvider{err: errors.New("provider down")}, 1, "https://app", "https://panel")
	if _, err := s.StartPurchase(context.Background(), "u", 5); err == nil {
		t.Fatal("want error")
	}
	if len(repo.failed) != 1 {
		t.Fatalf("purchase not marked failed: %v", repo.failed)
	}
}

func TestStudentsSeeOnlyTheirOwnPurchases(t *testing.T) {
	repo := newFakeRepo(0)
	repo.purchases["p1"] = Purchase{ID: "p1", UserID: "alice"}
	s := New(repo, nil, 1, "https://app", "https://panel")
	if _, err := s.Purchase(context.Background(), "bob", "p1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other student's purchase: %v", err)
	}
	if p, err := s.Purchase(context.Background(), "alice", "p1"); err != nil || p.ID != "p1" {
		t.Fatalf("own purchase: %+v %v", p, err)
	}
}

func centreRepo(balance int32) *fakeRepo {
	r := newFakeRepo(1) // a personal star that must never be touched
	r.centre = &Centre{ID: "c1", Name: "Cambridge Centre"}
	r.centreBalance = balance
	return r
}

func TestCentreStudentsSpendTheCentresStars(t *testing.T) {
	repo := centreRepo(2)
	s := New(repo, &fakeProvider{}, 1, "https://app", "https://panel")
	ctx := context.Background()
	o, err := s.Overview(ctx, "student")
	if err != nil || o.Source != "centre" || o.CentreName != "Cambridge Centre" || o.Balance != 2 || len(o.Packages) != 0 || o.PaymentsAvailable {
		t.Fatalf("overview: %+v %v", o, err)
	}
	if w, err := s.Spend(ctx, "student", ReasonFullMock, "mock-11111111"); err != nil || w.Balance != 1 {
		t.Fatalf("spend: %+v %v", w, err)
	}
	if repo.balance != 1 {
		t.Fatalf("personal balance changed to %d", repo.balance)
	}
	if _, err := s.StartPurchase(ctx, "student", 5); !errors.Is(err, ErrCentreManaged) {
		t.Fatalf("centre student buying: %v", err)
	}
}

func TestEmptyCentreTellsTheStudentToAskTheCentre(t *testing.T) {
	s := New(centreRepo(0), nil, 1, "https://app", "https://panel")
	if _, err := s.Spend(context.Background(), "student", ReasonFullMock, "mock-22222222"); !errors.Is(err, ErrCentreInsufficient) {
		t.Fatalf("empty centre: %v", err)
	}
}

func TestOnlyPlatformAdminsGrantAndCentreAdminsSeeTheirOwn(t *testing.T) {
	repo := centreRepo(0)
	s := New(repo, nil, 1, "https://app", "https://panel")
	ctx := context.Background()
	platform, own, other := Actor{AdminID: "p"}, Actor{AdminID: "a", CentreID: "c1"}, Actor{AdminID: "b", CentreID: "c2"}

	if _, err := s.CentreStars(ctx, other, "c1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("another centre's admin: %v", err)
	}
	if v, err := s.CentreStars(ctx, own, "c1"); err != nil || v.CanGrant {
		t.Fatalf("own centre: %+v %v", v, err)
	}
	if _, err := s.GrantCentreStars(ctx, own, "c1", 10, "grant-12345678", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("centre admin granting: %v", err)
	}
	for _, bad := range []int32{0, -5, 10001} {
		if _, err := s.GrantCentreStars(ctx, platform, "c1", bad, "grant-12345678", ""); !errors.Is(err, ErrInvalidGrant) {
			t.Fatalf("grant %d: %v", bad, err)
		}
	}
	v, err := s.GrantCentreStars(ctx, platform, "c1", 30, "grant-12345678", "Invoice 17")
	if err != nil || v.Balance != 30 || !v.CanGrant {
		t.Fatalf("grant: %+v %v", v, err)
	}
	if v, _ := s.GrantCentreStars(ctx, platform, "c1", 30, "grant-12345678", "Invoice 17"); v.Balance != 30 {
		t.Fatalf("repeated grant changed balance to %d", v.Balance)
	}
}

type checkingProvider struct {
	fakeProvider
	state PaymentState
}

func (c *checkingProvider) Status(context.Context, Purchase) (PaymentState, error) {
	return c.state, nil
}

type settlingRepo struct {
	*fakeRepo
	paid, failed int
}

func (r *settlingRepo) SettlePaid(_ context.Context, id, _ string) (Purchase, error) {
	r.paid++
	p := r.purchases[id]
	p.Status = "paid"
	return p, nil
}
func (r *settlingRepo) SettleFailed(_ context.Context, id, _ string) (Purchase, error) {
	r.failed++
	p := r.purchases[id]
	p.Status = "failed"
	return p, nil
}

func TestPricingIsSetByPlatformAdminsInWholeSom(t *testing.T) {
	repo := newFakeRepo(0)
	s := New(repo, &fakeProvider{}, 1, "https://app", "https://panel")
	ctx := context.Background()
	if _, err := s.SetPricing(ctx, Actor{AdminID: "a", CentreID: "c1"}, 2_500_000); !errors.Is(err, ErrForbidden) {
		t.Fatalf("centre admin setting price: %v", err)
	}
	for _, bad := range []int64{0, 99_900, 2_500_050, 2_000_000_000_00} {
		if _, err := s.SetPricing(ctx, Actor{AdminID: "p"}, bad); !errors.Is(err, ErrInvalidPrice) {
			t.Fatalf("price %d: %v", bad, err)
		}
	}
	if p, err := s.SetPricing(ctx, Actor{AdminID: "p"}, 2_500_000); err != nil || p.PricePerStarMinor != 2_500_000 {
		t.Fatalf("set price: %+v %v", p, err)
	}
	o, _ := s.Overview(ctx, "u")
	if len(o.Packages) != 3 || o.Packages[1].Stars != 5 || o.Packages[1].AmountMinor != 12_500_000 || !o.PaymentsAvailable || o.PricePerStarMinor != 2_500_000 {
		t.Fatalf("packages follow the price: %+v", o)
	}
}

func TestOnlyACentresOwnAdminBuysForItAndReturnsToThePanel(t *testing.T) {
	repo, prov := newFakeRepo(0), &fakeProvider{}
	s := New(repo, prov, 1, "https://mock-ai.uz", "https://panel.mock-ai.uz")
	ctx := context.Background()
	if _, err := s.StartCentrePurchase(ctx, Actor{AdminID: "p"}, "c1", 10); !errors.Is(err, ErrForbidden) {
		t.Fatalf("platform admin buying: %v", err)
	}
	if _, err := s.StartCentrePurchase(ctx, Actor{AdminID: "b", CentreID: "c2"}, "c1", 10); !errors.Is(err, ErrForbidden) {
		t.Fatalf("another centre's admin buying: %v", err)
	}
	if _, err := s.StartCentrePurchase(ctx, Actor{AdminID: "a", CentreID: "c1"}, "c1", MaxCentreStars+1); !errors.Is(err, ErrInvalidQuantity) {
		t.Fatalf("too many stars: %v", err)
	}
	out, err := s.StartCentrePurchase(ctx, Actor{AdminID: "a", CentreID: "c1"}, "c1", 40)
	if err != nil || out.Purchase.CentreID != "c1" || out.Purchase.UserID != "" || out.Purchase.AmountMinor != 120_000_000 {
		t.Fatalf("centre purchase: %+v %v", out, err)
	}
	if prov.lastURL != "https://panel.mock-ai.uz/?purchase="+out.Purchase.ID {
		t.Fatalf("return url %q", prov.lastURL)
	}
}

func TestPendingPurchaseIsSettledFromTheProvidersRecord(t *testing.T) {
	base := newFakeRepo(0)
	base.purchases["p1"] = Purchase{ID: "p1", UserID: "u", Status: "pending", Provider: "fake", ProviderReference: "pay-1"}
	repo := &settlingRepo{fakeRepo: base}
	prov := &checkingProvider{state: StatePaid}
	s := New(repo, prov, 1, "https://app", "https://panel")
	if p, err := s.Purchase(context.Background(), "u", "p1"); err != nil || p.Status != "paid" || repo.paid != 1 {
		t.Fatalf("refresh to paid: %+v %v (paid=%d)", p, err, repo.paid)
	}
	base.purchases["p2"] = Purchase{ID: "p2", UserID: "u", Status: "pending", Provider: "fake", ProviderReference: "pay-2"}
	prov.state = StatePending
	if p, _ := s.Purchase(context.Background(), "u", "p2"); p.Status != "pending" || repo.paid != 1 || repo.failed != 0 {
		t.Fatalf("still pending: %+v", p)
	}
}

func TestProviderFailureBecomesAFriendlyError(t *testing.T) {
	s := New(newFakeRepo(0), &fakeProvider{err: errors.New("inpay 500 internal")}, 1, "https://app", "https://panel")
	if _, err := s.StartPurchase(context.Background(), "u", 1); !errors.Is(err, ErrProvider) {
		t.Fatalf("provider failure: %v", err)
	}
}

func TestSweepCreditsLostPaymentsAndExpiresAbandonedCheckouts(t *testing.T) {
	base := newFakeRepo(0)
	now := time.Now()
	base.purchases["paid"] = Purchase{ID: "paid", UserID: "u", Status: "pending", Provider: "fake", ProviderReference: "pay-1", CreatedAt: now.Add(-time.Hour)}
	repo := &settlingRepo{fakeRepo: base}
	prov := &checkingProvider{state: StatePaid}
	s := New(repo, prov, 1, "https://app", "https://panel")
	if n := s.Sweep(context.Background(), now); n != 1 || repo.paid != 1 {
		t.Fatalf("lost payment: changed %d paid %d", n, repo.paid)
	}
	base.purchases["paid"] = Purchase{ID: "paid", Status: "paid"}
	base.purchases["old"] = Purchase{ID: "old", UserID: "u", Status: "pending", Provider: "fake", ProviderReference: "pay-2", CreatedAt: now.Add(-49 * time.Hour)}
	base.purchases["fresh"] = Purchase{ID: "fresh", UserID: "u", Status: "pending", Provider: "fake", ProviderReference: "pay-3", CreatedAt: now.Add(-time.Hour)}
	prov.state = StatePending
	if n := s.Sweep(context.Background(), now); n != 1 || repo.failed != 1 {
		t.Fatalf("abandoned checkout: changed %d failed %d", n, repo.failed)
	}
}
