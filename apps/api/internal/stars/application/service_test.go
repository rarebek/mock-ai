package application

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

type fakeRepo struct {
	centre        *Centre
	centreBalance int32
	granted       map[string]bool
	balance       int32
	spent         map[string]bool
	purchases     map[string]Purchase
	failed        []string
	ref           string
}

func newFakeRepo(balance int32) *fakeRepo {
	return &fakeRepo{balance: balance, spent: map[string]bool{}, purchases: map[string]Purchase{}, granted: map[string]bool{}}
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

var priced = []Package{{ID: "stars-5", Stars: 5, AmountMinor: 4_000_000, Currency: "UZS"}}

func TestSpendRules(t *testing.T) {
	s := New(newFakeRepo(1), nil, DefaultPackages(), 1, "https://app")
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
	s := New(newFakeRepo(1), nil, DefaultPackages(), 1, "https://app")
	o, err := s.Overview(context.Background(), "u")
	if err != nil || o.Balance != 1 || o.FullMockCost != 1 || o.PaymentsAvailable || len(o.Packages) == 0 {
		t.Fatalf("overview: %+v %v", o, err)
	}
	if _, err := s.StartPurchase(context.Background(), "u", "stars-5"); !errors.Is(err, ErrPaymentsOff) {
		t.Fatalf("purchase while off: %v", err)
	}
}

func TestUnpricedPackageIsNotSoldEvenWithAProvider(t *testing.T) {
	s := New(newFakeRepo(0), &fakeProvider{}, DefaultPackages(), 1, "https://app")
	if _, err := s.StartPurchase(context.Background(), "u", "stars-5"); !errors.Is(err, ErrPaymentsOff) {
		t.Fatalf("unpriced package: %v", err)
	}
}

func TestStartPurchaseHandsTheProviderAReturnURL(t *testing.T) {
	repo, prov := newFakeRepo(0), &fakeProvider{}
	s := New(repo, prov, priced, 1, "https://mock-ai.uz")
	out, err := s.StartPurchase(context.Background(), "u", "stars-5")
	if err != nil || out.CheckoutURL != "https://pay.example/checkout" {
		t.Fatalf("checkout: %+v %v", out, err)
	}
	if !strings.HasPrefix(prov.lastURL, "https://mock-ai.uz/?purchase="+out.Purchase.ID) || prov.lastAmnt != 4_000_000 {
		t.Fatalf("provider got url %q amount %d", prov.lastURL, prov.lastAmnt)
	}
	if repo.ref != "prov-1" {
		t.Fatalf("provider reference not stored: %q", repo.ref)
	}
	if _, err := s.StartPurchase(context.Background(), "u", "nope"); !errors.Is(err, ErrUnknownPackage) {
		t.Fatalf("unknown package: %v", err)
	}
}

func TestFailedCheckoutMarksThePurchaseFailed(t *testing.T) {
	repo := newFakeRepo(0)
	s := New(repo, &fakeProvider{err: errors.New("provider down")}, priced, 1, "https://app")
	if _, err := s.StartPurchase(context.Background(), "u", "stars-5"); err == nil {
		t.Fatal("want error")
	}
	if len(repo.failed) != 1 {
		t.Fatalf("purchase not marked failed: %v", repo.failed)
	}
}

func TestStudentsSeeOnlyTheirOwnPurchases(t *testing.T) {
	repo := newFakeRepo(0)
	repo.purchases["p1"] = Purchase{ID: "p1", UserID: "alice"}
	s := New(repo, nil, priced, 1, "https://app")
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
	s := New(repo, &fakeProvider{}, priced, 1, "https://app")
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
	if _, err := s.StartPurchase(ctx, "student", "stars-5"); !errors.Is(err, ErrCentreManaged) {
		t.Fatalf("centre student buying: %v", err)
	}
}

func TestEmptyCentreTellsTheStudentToAskTheCentre(t *testing.T) {
	s := New(centreRepo(0), nil, priced, 1, "https://app")
	if _, err := s.Spend(context.Background(), "student", ReasonFullMock, "mock-22222222"); !errors.Is(err, ErrCentreInsufficient) {
		t.Fatalf("empty centre: %v", err)
	}
}

func TestOnlyPlatformAdminsGrantAndCentreAdminsSeeTheirOwn(t *testing.T) {
	repo := centreRepo(0)
	s := New(repo, nil, priced, 1, "https://app")
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
