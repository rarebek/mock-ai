package application

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/own/mock-ai/apps/api/internal/platform/credits"
)

// fakeRepo keeps balances in memory with the same once-per-reference rule as
// the database. Database-level guarantees are tested in stars/postgres.
type fakeRepo struct {
	own           Balances
	centre        *Centre
	centreBal     Balances
	spent         map[string]bool
	granted       map[string]bool
	purchases     map[string]Purchase
	failed        []string
	ref           string
	pricing       Pricing
	paidMocks     map[string]bool // mock references with a paid star
	mockUses      map[string]int
	mockItemsSeen map[string]bool
}

func newFakeRepo(stars int32) *fakeRepo {
	return &fakeRepo{
		own:   Balances{Star: stars, Writing: 1, Speaking: 1},
		spent: map[string]bool{}, granted: map[string]bool{}, purchases: map[string]Purchase{},
		pricing:   Pricing{PricePerStarMinor: 3_000_000, WritingPriceMinor: 1_000_000, SpeakingPriceMinor: 1_200_000, Currency: "UZS"},
		paidMocks: map[string]bool{}, mockUses: map[string]int{}, mockItemsSeen: map[string]bool{},
	}
}

func take(b *Balances, k Kind, cost int32) bool {
	p := &b.Star
	switch k {
	case KindWriting:
		p = &b.Writing
	case KindSpeaking:
		p = &b.Speaking
	}
	if *p < cost {
		return false
	}
	*p -= cost
	return true
}

func (f *fakeRepo) EnsureWallet(context.Context, string, int32) (Balances, error) { return f.own, nil }
func (f *fakeRepo) Spend(_ context.Context, _ string, k Kind, reason, ref string, cost int32) (int32, error) {
	if f.spent[reason+ref] {
		return f.own.of(k), nil
	}
	if !take(&f.own, k, cost) {
		return f.own.of(k), ErrInsufficient
	}
	f.spent[reason+ref] = true
	return f.own.of(k), nil
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
func (f *fakeRepo) EnsureCentreWallet(context.Context, string) (Balances, error) {
	return f.centreBal, nil
}
func (f *fakeRepo) SpendFromCentre(_ context.Context, _, user string, k Kind, reason, ref string, cost int32) (int32, error) {
	if f.spent[user+reason+ref] {
		return f.centreBal.of(k), nil
	}
	if !take(&f.centreBal, k, cost) {
		return f.centreBal.of(k), ErrCentreInsufficient
	}
	f.spent[user+reason+ref] = true
	return f.centreBal.of(k), nil
}
func (f *fakeRepo) GrantCentre(_ context.Context, _, _ string, k Kind, count int32, ref, _ string) (int32, error) {
	if !f.granted[ref] {
		f.granted[ref] = true
		take(&f.centreBal, k, -count)
	}
	return f.centreBal.of(k), nil
}
func (f *fakeRepo) GrantUser(_ context.Context, _, _ string, k Kind, count int32, ref, _ string) (int32, error) {
	if !f.granted[ref] {
		f.granted[ref] = true
		take(&f.own, k, -count)
	}
	return f.own.of(k), nil
}
func (f *fakeRepo) CentreActivity(context.Context, string, int) ([]LedgerEntry, error) {
	return []LedgerEntry{}, nil
}
func (f *fakeRepo) Pricing(context.Context) (Pricing, error) { return f.pricing, nil }
func (f *fakeRepo) SetPricing(_ context.Context, p Pricing, _ string) (Pricing, error) {
	p.Currency = "UZS"
	f.pricing = p
	return p, nil
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
func (f *fakeRepo) UseMockAllowance(_ context.Context, _, mock string, k Kind, item string, limit int, _ time.Duration) (bool, error) {
	if !f.paidMocks[mock] {
		return false, nil
	}
	if f.mockItemsSeen[string(k)+item] {
		return true, nil
	}
	if f.mockUses[mock+string(k)] >= limit {
		return false, nil
	}
	f.mockUses[mock+string(k)]++
	f.mockItemsSeen[string(k)+item] = true
	return true, nil
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

func centreRepo(stars int32) *fakeRepo {
	r := newFakeRepo(1) // personal balances that must never be touched
	r.centre = &Centre{ID: "c1", Name: "Cambridge Centre"}
	r.centreBal = Balances{Star: stars}
	return r
}

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

func TestOverviewShowsEveryBalanceAndPricedPackages(t *testing.T) {
	s := New(newFakeRepo(2), &fakeProvider{}, 1, "https://app", "https://panel")
	o, err := s.Overview(context.Background(), "u")
	if err != nil || o.Source != "personal" || o.Balance != 2 || o.WritingBalance != 1 || o.SpeakingBalance != 1 || !o.PaymentsAvailable {
		t.Fatalf("overview: %+v %v", o, err)
	}
	prices := map[string]int64{}
	for _, p := range o.Packages {
		prices[p.ID] = p.AmountMinor
	}
	if len(o.Packages) != 9 || prices["stars-5"] != 15_000_000 || prices["writing-5"] != 5_000_000 || prices["speaking-10"] != 12_000_000 {
		t.Fatalf("packages: %+v", o.Packages)
	}
	off := New(newFakeRepo(0), nil, 1, "https://app", "https://panel")
	if o, _ := off.Overview(context.Background(), "u"); o.PaymentsAvailable {
		t.Fatal("payments shown as available without a provider")
	}
	if _, err := off.StartPurchase(context.Background(), "u", KindWriting, 1); !errors.Is(err, ErrPaymentsOff) {
		t.Fatalf("purchase while off: %v", err)
	}
}

func TestStartPurchaseUsesTheKindsPriceAndReturnURL(t *testing.T) {
	repo, prov := newFakeRepo(0), &fakeProvider{}
	s := New(repo, prov, 1, "https://mock-ai.uz", "https://panel")
	out, err := s.StartPurchase(context.Background(), "u", KindWriting, 5)
	if err != nil || out.CheckoutURL != "https://pay.example/checkout" || out.Purchase.Kind != KindWriting || out.Purchase.PackageID != "writing-5" {
		t.Fatalf("checkout: %+v %v", out, err)
	}
	if !strings.HasPrefix(prov.lastURL, "https://mock-ai.uz/?purchase="+out.Purchase.ID) || prov.lastAmnt != 5_000_000 {
		t.Fatalf("provider got url %q amount %d", prov.lastURL, prov.lastAmnt)
	}
	if repo.ref != "prov-1" {
		t.Fatalf("provider reference not stored: %q", repo.ref)
	}
	if _, err := s.StartPurchase(context.Background(), "u", "lottery", 1); !errors.Is(err, ErrInvalidKind) {
		t.Fatalf("unknown kind: %v", err)
	}
	for _, bad := range []int32{0, -1, MaxPersonalStars + 1} {
		if _, err := s.StartPurchase(context.Background(), "u", KindStar, bad); !errors.Is(err, ErrInvalidQuantity) {
			t.Fatalf("buying %d: %v", bad, err)
		}
	}
}

func TestChargeTakesOnePracticeCreditPerItem(t *testing.T) {
	repo := newFakeRepo(1)
	s := New(repo, nil, 1, "https://app", "https://panel")
	ctx := context.Background()
	if err := s.Charge(ctx, "u", credits.Writing, "writing:essay-1", ""); err != nil || repo.own.Writing != 0 {
		t.Fatalf("first essay: %v (writing left %d)", err, repo.own.Writing)
	}
	// Retrying the same essay (e.g. after a failed evaluation) is free.
	if err := s.Charge(ctx, "u", credits.Writing, "writing:essay-1", ""); err != nil {
		t.Fatalf("retry of the same essay: %v", err)
	}
	err := s.Charge(ctx, "u", credits.Writing, "writing:essay-2", "")
	if !errors.Is(err, credits.ErrNoCredits) || credits.Message(err) != "You have no Writing checks left. Get more on the Stars page." {
		t.Fatalf("second essay without credits: %v", err)
	}
	if repo.own.Star != 1 || repo.own.Speaking != 1 {
		t.Fatalf("other balances touched: %+v", repo.own)
	}
	if err := s.Charge(ctx, "u", credits.Speaking, "speaking:s-1", ""); err != nil || repo.own.Speaking != 0 {
		t.Fatalf("speaking: %v", err)
	}
}

func TestPaidFullMockCoversItsOwnWritingAndSpeaking(t *testing.T) {
	repo := newFakeRepo(1)
	repo.own.Writing, repo.own.Speaking = 0, 0
	repo.paidMocks["mock-paid-0001"] = true
	s := New(repo, nil, 1, "https://app", "https://panel")
	ctx := context.Background()
	for _, item := range []string{"writing:task1", "writing:task2", "writing:task1"} {
		if err := s.Charge(ctx, "u", credits.Writing, item, "mock-paid-0001"); err != nil {
			t.Fatalf("mock %s: %v", item, err)
		}
	}
	// A third essay is beyond the mock's allowance and needs a credit.
	if err := s.Charge(ctx, "u", credits.Writing, "writing:task3", "mock-paid-0001"); !errors.Is(err, credits.ErrNoCredits) {
		t.Fatalf("beyond allowance: %v", err)
	}
	// A mock reference nobody paid for gives nothing.
	if err := s.Charge(ctx, "u", credits.Speaking, "speaking:x", "mock-unpaid-01"); !errors.Is(err, credits.ErrNoCredits) {
		t.Fatalf("unpaid mock: %v", err)
	}
}

func TestCentreStudentsUseTheCentresBalances(t *testing.T) {
	repo := centreRepo(2)
	repo.centreBal.Writing = 1
	s := New(repo, &fakeProvider{}, 1, "https://app", "https://panel")
	ctx := context.Background()
	o, err := s.Overview(ctx, "student")
	if err != nil || o.Source != "centre" || o.CentreName != "Cambridge Centre" || o.Balance != 2 || o.WritingBalance != 1 || len(o.Packages) != 0 || o.PaymentsAvailable {
		t.Fatalf("overview: %+v %v", o, err)
	}
	if w, err := s.Spend(ctx, "student", ReasonFullMock, "mock-11111111"); err != nil || w.Balance != 1 {
		t.Fatalf("spend: %+v %v", w, err)
	}
	if err := s.Charge(ctx, "student", credits.Writing, "writing:e1", ""); err != nil || repo.centreBal.Writing != 0 {
		t.Fatalf("centre writing check: %v", err)
	}
	err = s.Charge(ctx, "student", credits.Speaking, "speaking:s1", "")
	if !errors.Is(err, credits.ErrNoCredits) || !strings.Contains(credits.Message(err), "learning centre has no Speaking interviews") {
		t.Fatalf("centre without speaking: %v", err)
	}
	if repo.own != (Balances{Star: 1, Writing: 1, Speaking: 1}) {
		t.Fatalf("personal balances changed: %+v", repo.own)
	}
	if _, err := s.StartPurchase(ctx, "student", KindStar, 5); !errors.Is(err, ErrCentreManaged) {
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
	if v, err := s.CentreStars(ctx, own, "c1"); err != nil || v.CanGrant || !v.CanBuy {
		t.Fatalf("own centre: %+v %v", v, err)
	}
	if _, err := s.GrantCentreStars(ctx, own, "c1", KindStar, 10, "grant-12345678", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("centre admin granting: %v", err)
	}
	for _, bad := range []int32{0, -5, 10001} {
		if _, err := s.GrantCentreStars(ctx, platform, "c1", KindStar, bad, "grant-12345678", ""); !errors.Is(err, ErrInvalidGrant) {
			t.Fatalf("grant %d: %v", bad, err)
		}
	}
	v, err := s.GrantCentreStars(ctx, platform, "c1", KindSpeaking, 30, "grant-12345678", "Invoice 17")
	if err != nil || v.SpeakingBalance != 30 || v.Balance != 0 || !v.CanGrant {
		t.Fatalf("grant: %+v %v", v, err)
	}
	if v, _ := s.GrantCentreStars(ctx, platform, "c1", KindSpeaking, 30, "grant-12345678", "Invoice 17"); v.SpeakingBalance != 30 {
		t.Fatalf("repeated grant changed balance to %d", v.SpeakingBalance)
	}
}

func TestPlatformAdminAddsToAStudentsOwnWalletOnce(t *testing.T) {
	repo := newFakeRepo(1)
	s := New(repo, nil, 1, "https://app", "https://panel")
	ctx := context.Background()
	platform, centreAdmin := Actor{AdminID: "p"}, Actor{AdminID: "a", CentreID: "c1"}
	if _, err := s.GrantUserStars(ctx, centreAdmin, "student", KindStar, 5, "grant-12345678", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("centre admin granting: %v", err)
	}
	if _, err := s.GrantUserStars(ctx, platform, "student", KindStar, 0, "grant-12345678", ""); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("zero grant: %v", err)
	}
	for i := 0; i < 2; i++ { // a double click adds once
		v, err := s.GrantUserStars(ctx, platform, "student", KindWriting, 4, "grant-12345678", "Promo")
		if err != nil || v.WritingBalance != 5 || v.Balance != 1 || v.CentreName != "" {
			t.Fatalf("grant %d: %+v %v", i, v, err)
		}
	}
	repo.centre = &Centre{ID: "c1", Name: "Alpha"}
	if v, _ := s.StudentStars(ctx, platform, "student"); v.CentreName != "Alpha" || v.WritingBalance != 5 {
		t.Fatalf("centre student: %+v", v)
	}
}

func TestPricingIsSetByPlatformAdminsInWholeSom(t *testing.T) {
	repo := newFakeRepo(0)
	s := New(repo, &fakeProvider{}, 1, "https://app", "https://panel")
	ctx := context.Background()
	if _, err := s.SetPricing(ctx, Actor{AdminID: "a", CentreID: "c1"}, Pricing{PricePerStarMinor: 2_500_000}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("centre admin setting price: %v", err)
	}
	for _, bad := range []Pricing{{PricePerStarMinor: 99_900}, {WritingPriceMinor: 2_500_050}, {SpeakingPriceMinor: 2_000_000_000_00}} {
		if _, err := s.SetPricing(ctx, Actor{AdminID: "p"}, bad); !errors.Is(err, ErrInvalidPrice) {
			t.Fatalf("price %+v: %v", bad, err)
		}
	}
	// Only the writing price changes; the others keep their values.
	p, err := s.SetPricing(ctx, Actor{AdminID: "p"}, Pricing{WritingPriceMinor: 800_000})
	if err != nil || p.WritingPriceMinor != 800_000 || p.PricePerStarMinor != 3_000_000 || p.SpeakingPriceMinor != 1_200_000 {
		t.Fatalf("set price: %+v %v", p, err)
	}
}

func TestOnlyACentresOwnAdminBuysForItAndReturnsToThePanel(t *testing.T) {
	repo, prov := newFakeRepo(0), &fakeProvider{}
	s := New(repo, prov, 1, "https://mock-ai.uz", "https://panel.mock-ai.uz")
	ctx := context.Background()
	if _, err := s.StartCentrePurchase(ctx, Actor{AdminID: "p"}, "c1", KindStar, 10); !errors.Is(err, ErrForbidden) {
		t.Fatalf("platform admin buying: %v", err)
	}
	if _, err := s.StartCentrePurchase(ctx, Actor{AdminID: "b", CentreID: "c2"}, "c1", KindStar, 10); !errors.Is(err, ErrForbidden) {
		t.Fatalf("another centre's admin buying: %v", err)
	}
	if _, err := s.StartCentrePurchase(ctx, Actor{AdminID: "a", CentreID: "c1"}, "c1", KindStar, MaxCentreStars+1); !errors.Is(err, ErrInvalidQuantity) {
		t.Fatalf("too many: %v", err)
	}
	out, err := s.StartCentrePurchase(ctx, Actor{AdminID: "a", CentreID: "c1"}, "c1", KindSpeaking, 40)
	if err != nil || out.Purchase.CentreID != "c1" || out.Purchase.UserID != "" || out.Purchase.AmountMinor != 48_000_000 || out.Purchase.Kind != KindSpeaking {
		t.Fatalf("centre purchase: %+v %v", out, err)
	}
	if prov.lastURL != "https://panel.mock-ai.uz/?purchase="+out.Purchase.ID {
		t.Fatalf("return url %q", prov.lastURL)
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
	r.purchases[id] = p
	return p, nil
}
func (r *settlingRepo) SettleFailed(_ context.Context, id, _ string) (Purchase, error) {
	r.failed++
	p := r.purchases[id]
	p.Status = "failed"
	r.purchases[id] = p
	return p, nil
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
	base.purchases["old"] = Purchase{ID: "old", UserID: "u", Status: "pending", Provider: "fake", ProviderReference: "pay-2", CreatedAt: now.Add(-49 * time.Hour)}
	base.purchases["fresh"] = Purchase{ID: "fresh", UserID: "u", Status: "pending", Provider: "fake", ProviderReference: "pay-3", CreatedAt: now.Add(-time.Hour)}
	prov.state = StatePending
	if n := s.Sweep(context.Background(), now); n != 1 || repo.failed != 1 {
		t.Fatalf("abandoned checkout: changed %d failed %d", n, repo.failed)
	}
}

func TestProviderFailureBecomesAFriendlyError(t *testing.T) {
	repo := newFakeRepo(0)
	s := New(repo, &fakeProvider{err: errors.New("inpay 500 internal")}, 1, "https://app", "https://panel")
	if _, err := s.StartPurchase(context.Background(), "u", KindStar, 1); !errors.Is(err, ErrProvider) {
		t.Fatalf("provider failure: %v", err)
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
