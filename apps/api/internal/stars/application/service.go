// Package application holds the rules for what students pay with:
//
//   - stars, spent on full mock exams;
//   - Writing checks and Speaking interviews, spent on AI-marked practice.
//
// A learning centre's students spend their centre's balances; everyone else
// spends their own. The service never talks to a payment company directly. It
// talks to a Provider (see provider.go), so adding one means writing a
// Provider and selecting it in configuration; nothing here changes.
package application

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/own/mock-ai/apps/api/internal/platform/credits"
)

var (
	ErrInsufficient   = errors.New("you have no stars left; get more stars to start a full mock")
	ErrUnknownPackage = errors.New("that package does not exist")
	ErrUnknownPurpose = errors.New("stars cannot be spent on that")
	ErrInvalidRef     = errors.New("a spend needs a client reference of 8–64 letters, digits, dashes or underscores")
	ErrNotFound       = errors.New("purchase not found")
	ErrPaymentsOff    = errors.New("payments are coming soon; this cannot be bought yet")
	// A learning centre's students spend the centre's balances, not their own.
	ErrCentreInsufficient = errors.New("your learning centre has no stars left; ask your centre to add more")
	ErrCentreManaged      = errors.New("your learning centre provides these, so they cannot be bought here")
	ErrForbidden          = errors.New("you cannot manage this centre's balance")
	ErrInvalidGrant       = errors.New("add between 1 and 10000, with a note of at most 200 characters")
	ErrInvalidQuantity    = errors.New("choose how many to buy")
	ErrInvalidKind        = errors.New("choose stars, Writing checks or Speaking interviews")
	ErrInvalidPrice       = errors.New("each price must be a whole number of so'm between 1 000 and 10 000 000")
	ErrProvider           = errors.New("the payment page could not be opened; please try again in a moment")
)

// Reasons recorded in the ledger. They match the database CHECK constraint.
const (
	ReasonWelcome           = "welcome"
	ReasonFullMock          = "full_mock"
	ReasonPurchase          = "purchase"
	ReasonGrant             = "grant"
	ReasonRefund            = "refund"
	ReasonWritingCheck      = "writing_check"
	ReasonSpeakingInterview = "speaking_interview"
)

// What a star can be spent on, and how many stars each costs.
var spendCost = map[string]int32{
	ReasonFullMock: 1,
}

// A paid full mock covers its own Writing (Task 1 and Task 2) and Speaking
// sections, with room for one restart of the interview, for this long.
const (
	mockWritingAllowance  = 2
	mockSpeakingAllowance = 2
	mockAllowanceWindow   = 48 * time.Hour
)

type Wallet struct {
	Balance int32 `json:"balance"`
}

type Purchase struct {
	ID                string     `json:"id"`
	UserID            string     `json:"-"`
	Kind              Kind       `json:"kind"`
	PackageID         string     `json:"package_id"`
	Stars             int32      `json:"stars"` // how many of Kind
	AmountMinor       int64      `json:"amount_minor"`
	Currency          string     `json:"currency"`
	Provider          string     `json:"provider"`
	ProviderReference string     `json:"-"`
	CentreID          string     `json:"-"`
	AdminID           string     `json:"-"`
	Status            string     `json:"status"`
	FailureReason     string     `json:"failure_reason,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	SettledAt         *time.Time `json:"settled_at,omitempty"`
}

// Repository is the storage the service needs. Every method that changes a
// balance runs in a single transaction and is idempotent on its reference.
type Repository interface {
	// EnsureWallet creates the wallet with `welcome` of each kind the first
	// time a user is seen and returns its balances.
	EnsureWallet(ctx context.Context, userID string, welcome int32) (Balances, error)
	// Spend takes cost of kind once per (user, reason, reference). A repeat
	// returns the balance without charging again. ErrInsufficient when short.
	Spend(ctx context.Context, userID string, kind Kind, reason, reference string, cost int32) (int32, error)
	CreatePurchase(ctx context.Context, p Purchase) (Purchase, error)
	SetProviderReference(ctx context.Context, purchaseID, reference string) error
	Purchase(ctx context.Context, purchaseID string) (Purchase, error)
	// SettlePaid marks a pending purchase paid and credits it, once.
	SettlePaid(ctx context.Context, purchaseID, providerReference string) (Purchase, error)
	SettleFailed(ctx context.Context, purchaseID, reason string) (Purchase, error)

	// StudentCentre reports the learning centre a student belongs to, if any.
	StudentCentre(ctx context.Context, userID string) (Centre, bool, error)
	EnsureCentreWallet(ctx context.Context, centreID string) (Balances, error)
	// SpendFromCentre takes cost of kind from the centre once per (student,
	// reason, reference) and records which student used it.
	// ErrCentreInsufficient when short.
	SpendFromCentre(ctx context.Context, centreID, userID string, kind Kind, reason, reference string, cost int32) (int32, error)
	// GrantCentre adds count of kind to a centre once per reference.
	GrantCentre(ctx context.Context, centreID, adminID string, kind Kind, count int32, reference, note string) (int32, error)
	CentreActivity(ctx context.Context, centreID string, limit int) ([]LedgerEntry, error)
	// GrantUser adds count of kind to a student's own wallet once per reference.
	GrantUser(ctx context.Context, userID, adminID string, kind Kind, count int32, reference, note string) (int32, error)

	Pricing(ctx context.Context) (Pricing, error)
	SetPricing(ctx context.Context, p Pricing, adminID string) (Pricing, error)
	RecentPurchases(ctx context.Context, limit int) ([]PurchaseRow, error)
	// PendingPurchases lists the oldest unsettled purchases, for the sweep.
	PendingPurchases(ctx context.Context, limit int) ([]Purchase, error)
	// UseMockAllowance lets an AI call inside a paid full mock go uncharged.
	UseMockAllowance(ctx context.Context, userID, mockReference string, kind Kind, item string, limit int, maxAge time.Duration) (bool, error)
}

// PurchaseRow is a purchase as the platform administrator sees it.
type PurchaseRow struct {
	Purchase
	Buyer     string `json:"buyer"`
	BuyerKind string `json:"buyer_kind"` // "student" or "centre"
}

// Centre is the learning centre a student belongs to.
type Centre struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// LedgerEntry is one change to a centre's balance, for its administrators.
type LedgerEntry struct {
	ID           string    `json:"id"`
	Kind         Kind      `json:"kind"`
	Reason       string    `json:"reason"`
	Delta        int32     `json:"delta"`
	BalanceAfter int32     `json:"balance_after"`
	StudentName  string    `json:"student_name,omitempty"`
	Note         string    `json:"note,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type Service struct {
	repo            Repository
	provider        Provider
	welcome         int32
	returnBase      string
	adminReturnBase string
}

// New builds the service. welcome is how many of each kind a new account
// starts with. After paying, the provider sends a student to
// <returnBase>/?purchase=<id>#stars and a centre administrator to
// <adminReturnBase>/?purchase=<id>.
func New(repo Repository, provider Provider, welcome int32, returnBase, adminReturnBase string) *Service {
	if provider == nil {
		provider = Unavailable{}
	}
	if welcome < 0 {
		welcome = 0
	}
	return &Service{repo: repo, provider: provider, welcome: welcome, returnBase: returnBase, adminReturnBase: adminReturnBase}
}

// Overview is everything the stars page needs in one request.
//
// Source is "personal" for a self-serve student and "centre" for a learning
// centre's student, whose balances are the centre's and who cannot buy.
type Overview struct {
	Source             string    `json:"source"`
	CentreName         string    `json:"centre_name,omitempty"`
	Balance            int32     `json:"balance"`
	WritingBalance     int32     `json:"writing_balance"`
	SpeakingBalance    int32     `json:"speaking_balance"`
	FullMockCost       int32     `json:"full_mock_cost"`
	PricePerStarMinor  int64     `json:"price_per_star_minor"`
	WritingPriceMinor  int64     `json:"writing_price_minor"`
	SpeakingPriceMinor int64     `json:"speaking_price_minor"`
	Currency           string    `json:"currency"`
	MaxStars           int32     `json:"max_stars"`
	Packages           []Package `json:"packages"`
	PaymentsAvailable  bool      `json:"payments_available"`
}

func (s *Service) salesOpen(p Pricing) bool { return s.provider.Available() && p.PricePerStarMinor > 0 }

func (s *Service) Overview(ctx context.Context, userID string) (Overview, error) {
	centre, inCentre, err := s.repo.StudentCentre(ctx, userID)
	if err != nil {
		return Overview{}, err
	}
	var b Balances
	if inCentre {
		b, err = s.repo.EnsureCentreWallet(ctx, centre.ID)
	} else {
		b, err = s.repo.EnsureWallet(ctx, userID, s.welcome)
	}
	if err != nil {
		return Overview{}, err
	}
	o := Overview{Balance: b.Star, WritingBalance: b.Writing, SpeakingBalance: b.Speaking, FullMockCost: spendCost[ReasonFullMock], Packages: []Package{}}
	if inCentre {
		o.Source, o.CentreName = "centre", centre.Name
		return o, nil
	}
	pricing, err := s.repo.Pricing(ctx)
	if err != nil {
		return Overview{}, err
	}
	o.Source = "personal"
	o.PricePerStarMinor, o.WritingPriceMinor, o.SpeakingPriceMinor = pricing.PricePerStarMinor, pricing.WritingPriceMinor, pricing.SpeakingPriceMinor
	o.Currency, o.MaxStars = pricing.Currency, MaxPersonalStars
	o.Packages = packagesFor(pricing)
	o.PaymentsAvailable = s.salesOpen(pricing)
	return o, nil
}

var referencePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// Spend charges a star for a full mock a student is about to start.
// reference is chosen by the client (the mock's id), so a retried request
// cannot charge twice, and the mock's Writing and Speaking sections can later
// prove it was paid for.
func (s *Service) Spend(ctx context.Context, userID, purpose, reference string) (Wallet, error) {
	cost, ok := spendCost[purpose]
	if !ok {
		return Wallet{}, ErrUnknownPurpose
	}
	if !referencePattern.MatchString(reference) {
		return Wallet{}, ErrInvalidRef
	}
	centre, inCentre, err := s.repo.StudentCentre(ctx, userID)
	if err != nil {
		return Wallet{}, err
	}
	if inCentre {
		if _, err := s.repo.EnsureCentreWallet(ctx, centre.ID); err != nil {
			return Wallet{}, err
		}
		balance, err := s.repo.SpendFromCentre(ctx, centre.ID, userID, KindStar, purpose, reference, cost)
		if err != nil {
			return Wallet{}, err
		}
		return Wallet{Balance: balance}, nil
	}
	if _, err := s.repo.EnsureWallet(ctx, userID, s.welcome); err != nil {
		return Wallet{}, err
	}
	balance, err := s.repo.Spend(ctx, userID, KindStar, purpose, reference, cost)
	if err != nil {
		return Wallet{}, err
	}
	return Wallet{Balance: balance}, nil
}

// Charge pays for one AI-marked Writing evaluation or Speaking interview
// (credits.Charger). Inside a paid full mock the mock's allowance is used;
// otherwise one practice credit of that kind is taken, from the centre for a
// centre's student. The same item is never charged twice.
func (s *Service) Charge(ctx context.Context, userID string, kind credits.Kind, item, fullMock string) error {
	k, reason, label, limit := KindWriting, ReasonWritingCheck, "Writing checks", mockWritingAllowance
	switch kind {
	case credits.Writing:
	case credits.Speaking:
		k, reason, label, limit = KindSpeaking, ReasonSpeakingInterview, "Speaking interviews", mockSpeakingAllowance
	default:
		return ErrInvalidKind
	}
	if fullMock != "" && referencePattern.MatchString(fullMock) {
		ok, err := s.repo.UseMockAllowance(ctx, userID, fullMock, k, item, limit, mockAllowanceWindow)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	centre, inCentre, err := s.repo.StudentCentre(ctx, userID)
	if err != nil {
		return err
	}
	if inCentre {
		if _, err := s.repo.EnsureCentreWallet(ctx, centre.ID); err != nil {
			return err
		}
		_, err := s.repo.SpendFromCentre(ctx, centre.ID, userID, k, reason, item, 1)
		if errors.Is(err, ErrCentreInsufficient) {
			return fmt.Errorf("%w: Your learning centre has no %s left. Ask your centre to add more.", credits.ErrNoCredits, label)
		}
		return err
	}
	if _, err := s.repo.EnsureWallet(ctx, userID, s.welcome); err != nil {
		return err
	}
	_, err = s.repo.Spend(ctx, userID, k, reason, item, 1)
	if errors.Is(err, ErrInsufficient) {
		return fmt.Errorf("%w: You have no %s left. Get more on the Stars page.", credits.ErrNoCredits, label)
	}
	return err
}

// CheckoutResult tells the client where to send the buyer to pay.
type CheckoutResult struct {
	Purchase    Purchase `json:"purchase"`
	CheckoutURL string   `json:"checkout_url"`
}

// StartPurchase records a student's pending purchase and asks the provider
// for a checkout page. Credits are added later, when the provider confirms
// payment.
func (s *Service) StartPurchase(ctx context.Context, userID string, kind Kind, count int32) (CheckoutResult, error) {
	if !validKind(kind) {
		return CheckoutResult{}, ErrInvalidKind
	}
	if _, inCentre, err := s.repo.StudentCentre(ctx, userID); err != nil {
		return CheckoutResult{}, err
	} else if inCentre {
		return CheckoutResult{}, ErrCentreManaged
	}
	if count < 1 || count > MaxPersonalStars {
		return CheckoutResult{}, ErrInvalidQuantity
	}
	return s.checkout(ctx, Purchase{UserID: userID, Kind: kind, Stars: count}, func(id string) string {
		return s.returnBase + "/?purchase=" + id + "#stars"
	})
}

// StartCentrePurchase lets a centre's own administrator buy for the centre's
// students. Platform administrators add to a centre with a grant instead.
func (s *Service) StartCentrePurchase(ctx context.Context, actor Actor, centreID string, kind Kind, count int32) (CheckoutResult, error) {
	if actor.platform() || actor.CentreID != centreID {
		return CheckoutResult{}, ErrForbidden
	}
	if !validKind(kind) {
		return CheckoutResult{}, ErrInvalidKind
	}
	if count < 1 || count > MaxCentreStars {
		return CheckoutResult{}, ErrInvalidQuantity
	}
	if _, err := s.repo.EnsureCentreWallet(ctx, centreID); err != nil {
		return CheckoutResult{}, err
	}
	return s.checkout(ctx, Purchase{CentreID: centreID, AdminID: actor.AdminID, Kind: kind, Stars: count}, func(id string) string {
		return s.adminReturnBase + "/?purchase=" + id
	})
}

func (s *Service) checkout(ctx context.Context, p Purchase, returnURL func(id string) string) (CheckoutResult, error) {
	pricing, err := s.repo.Pricing(ctx)
	if err != nil {
		return CheckoutResult{}, err
	}
	if !s.salesOpen(pricing) || pricing.of(p.Kind) <= 0 {
		return CheckoutResult{}, ErrPaymentsOff
	}
	p.PackageID = packageID(p.Kind, p.Stars)
	p.AmountMinor = int64(p.Stars) * pricing.of(p.Kind)
	p.Currency = pricing.Currency
	p.Provider = s.provider.Name()
	purchase, err := s.repo.CreatePurchase(ctx, p)
	if err != nil {
		return CheckoutResult{}, err
	}
	checkout, err := s.provider.Checkout(ctx, CheckoutRequest{Purchase: purchase, ReturnURL: returnURL(purchase.ID)})
	if err != nil {
		_, _ = s.repo.SettleFailed(ctx, purchase.ID, "checkout could not be created")
		if errors.Is(err, ErrPaymentsOff) {
			return CheckoutResult{}, err
		}
		return CheckoutResult{}, fmt.Errorf("%w: %v", ErrProvider, err)
	}
	if checkout.Reference != "" {
		if err := s.repo.SetProviderReference(ctx, purchase.ID, checkout.Reference); err != nil {
			return CheckoutResult{}, err
		}
		purchase.ProviderReference = checkout.Reference
	}
	return CheckoutResult{Purchase: purchase, CheckoutURL: checkout.URL}, nil
}

// Purchase returns one of the student's own purchases (for the return page),
// asking the provider for its latest state if it is still pending.
func (s *Service) Purchase(ctx context.Context, userID, purchaseID string) (Purchase, error) {
	p, err := s.repo.Purchase(ctx, purchaseID)
	if err != nil {
		return Purchase{}, err
	}
	if p.UserID == "" || p.UserID != userID {
		return Purchase{}, ErrNotFound
	}
	return s.refresh(ctx, p), nil
}

// CentrePurchase is the admin panel's view of one of its centre's purchases.
func (s *Service) CentrePurchase(ctx context.Context, actor Actor, purchaseID string) (Purchase, error) {
	p, err := s.repo.Purchase(ctx, purchaseID)
	if err != nil {
		return Purchase{}, err
	}
	if p.CentreID == "" || (!actor.platform() && actor.CentreID != p.CentreID) {
		return Purchase{}, ErrNotFound
	}
	return s.refresh(ctx, p), nil
}

// refresh settles a pending purchase from the provider's own record when the
// provider can be asked directly. The callback normally does this first; this
// covers a slow or lost callback while the buyer waits on the return page.
func (s *Service) refresh(ctx context.Context, p Purchase) Purchase {
	checker, ok := s.provider.(StatusChecker)
	if p.Status != "pending" || !ok || p.Provider != s.provider.Name() || p.ProviderReference == "" {
		return p
	}
	state, err := checker.Status(ctx, p)
	if err != nil {
		return p
	}
	var settled Purchase
	switch state {
	case StatePaid:
		settled, err = s.repo.SettlePaid(ctx, p.ID, p.ProviderReference)
	case StateFailed:
		settled, err = s.repo.SettleFailed(ctx, p.ID, "payment was not completed")
	default:
		return p
	}
	if err != nil {
		return p
	}
	return settled
}

// Provider is the configured payment provider; the HTTP layer routes its
// server-to-server callbacks to it.
func (s *Service) Provider() Provider { return s.provider }

// Settler is what a Provider may do to a purchase once it has verified a
// callback. It never lets a provider touch balances directly.
func (s *Service) Settler() Settler { return settler{s.repo} }

type settler struct{ repo Repository }

func (t settler) Purchase(ctx context.Context, purchaseID string) (Purchase, error) {
	return t.repo.Purchase(ctx, purchaseID)
}
func (t settler) MarkPaid(ctx context.Context, purchaseID, providerReference string) (Purchase, error) {
	return t.repo.SettlePaid(ctx, purchaseID, providerReference)
}
func (t settler) MarkFailed(ctx context.Context, purchaseID, reason string) (Purchase, error) {
	return t.repo.SettleFailed(ctx, purchaseID, reason)
}

// Actor is an administrator acting on a centre's balances. CentreID is empty
// for a platform administrator, who may manage every centre; a centre's own
// administrator may only see their centre.
type Actor struct {
	AdminID  string
	CentreID string
}

func (a Actor) platform() bool { return a.CentreID == "" }

// CentreStars is a centre's balances and recent activity.
type CentreStars struct {
	CentreID           string        `json:"centre_id"`
	Balance            int32         `json:"balance"`
	WritingBalance     int32         `json:"writing_balance"`
	SpeakingBalance    int32         `json:"speaking_balance"`
	FullMockCost       int32         `json:"full_mock_cost"`
	CanGrant           bool          `json:"can_grant"`
	CanBuy             bool          `json:"can_buy"`
	PricePerStarMinor  int64         `json:"price_per_star_minor"`
	WritingPriceMinor  int64         `json:"writing_price_minor"`
	SpeakingPriceMinor int64         `json:"speaking_price_minor"`
	Currency           string        `json:"currency"`
	MaxStars           int32         `json:"max_stars"`
	PaymentsAvailable  bool          `json:"payments_available"`
	Activity           []LedgerEntry `json:"activity"`
}

func (s *Service) CentreStars(ctx context.Context, actor Actor, centreID string) (CentreStars, error) {
	if !actor.platform() && actor.CentreID != centreID {
		return CentreStars{}, ErrForbidden
	}
	b, err := s.repo.EnsureCentreWallet(ctx, centreID)
	if err != nil {
		return CentreStars{}, err
	}
	activity, err := s.repo.CentreActivity(ctx, centreID, 50)
	if err != nil {
		return CentreStars{}, err
	}
	pricing, err := s.repo.Pricing(ctx)
	if err != nil {
		return CentreStars{}, err
	}
	return CentreStars{
		CentreID: centreID, Balance: b.Star, WritingBalance: b.Writing, SpeakingBalance: b.Speaking, FullMockCost: spendCost[ReasonFullMock],
		CanGrant: actor.platform(), CanBuy: !actor.platform(),
		PricePerStarMinor: pricing.PricePerStarMinor, WritingPriceMinor: pricing.WritingPriceMinor, SpeakingPriceMinor: pricing.SpeakingPriceMinor,
		Currency: pricing.Currency, MaxStars: MaxCentreStars, PaymentsAvailable: s.salesOpen(pricing), Activity: activity,
	}, nil
}

// GrantCentreStars tops up a centre without payment (e.g. paid by bank
// transfer); platform administrators only.
func (s *Service) GrantCentreStars(ctx context.Context, actor Actor, centreID string, kind Kind, count int32, reference, note string) (CentreStars, error) {
	if !actor.platform() {
		return CentreStars{}, ErrForbidden
	}
	if !validKind(kind) {
		return CentreStars{}, ErrInvalidKind
	}
	if count < 1 || count > 10000 || len([]rune(note)) > 200 {
		return CentreStars{}, ErrInvalidGrant
	}
	if !referencePattern.MatchString(reference) {
		return CentreStars{}, ErrInvalidRef
	}
	if _, err := s.repo.EnsureCentreWallet(ctx, centreID); err != nil {
		return CentreStars{}, err
	}
	if _, err := s.repo.GrantCentre(ctx, centreID, actor.AdminID, kind, count, reference, note); err != nil {
		return CentreStars{}, err
	}
	return s.CentreStars(ctx, actor, centreID)
}

// StudentStars is a student's own wallet as a platform administrator sees it.
// CentreName is set when the student's centre pays instead, so these
// balances are used only after the student leaves it.
type StudentStars struct {
	Balance         int32  `json:"balance"`
	WritingBalance  int32  `json:"writing_balance"`
	SpeakingBalance int32  `json:"speaking_balance"`
	CentreName      string `json:"centre_name,omitempty"`
}

func (s *Service) StudentStars(ctx context.Context, actor Actor, userID string) (StudentStars, error) {
	if !actor.platform() {
		return StudentStars{}, ErrForbidden
	}
	b, err := s.repo.EnsureWallet(ctx, userID, s.welcome)
	if err != nil {
		return StudentStars{}, err
	}
	centre, _, err := s.repo.StudentCentre(ctx, userID)
	if err != nil {
		return StudentStars{}, err
	}
	return StudentStars{Balance: b.Star, WritingBalance: b.Writing, SpeakingBalance: b.Speaking, CentreName: centre.Name}, nil
}

// GrantUserStars adds to a student's own wallet without payment; platform
// administrators only.
func (s *Service) GrantUserStars(ctx context.Context, actor Actor, userID string, kind Kind, count int32, reference, note string) (StudentStars, error) {
	if !actor.platform() {
		return StudentStars{}, ErrForbidden
	}
	if !validKind(kind) {
		return StudentStars{}, ErrInvalidKind
	}
	if count < 1 || count > 10000 || len([]rune(note)) > 200 {
		return StudentStars{}, ErrInvalidGrant
	}
	if !referencePattern.MatchString(reference) {
		return StudentStars{}, ErrInvalidRef
	}
	if _, err := s.repo.EnsureWallet(ctx, userID, s.welcome); err != nil {
		return StudentStars{}, err
	}
	if _, err := s.repo.GrantUser(ctx, userID, actor.AdminID, kind, count, reference, note); err != nil {
		return StudentStars{}, err
	}
	return s.StudentStars(ctx, actor, userID)
}

// Pricing is the current price list.
func (s *Service) Pricing(ctx context.Context) (Pricing, error) { return s.repo.Pricing(ctx) }

// SetPricing changes prices; platform administrators only. A zero field keeps
// its current price. Purchases already started keep the amount they were
// created with.
func (s *Service) SetPricing(ctx context.Context, actor Actor, p Pricing) (Pricing, error) {
	if !actor.platform() {
		return Pricing{}, ErrForbidden
	}
	current, err := s.repo.Pricing(ctx)
	if err != nil {
		return Pricing{}, err
	}
	for _, f := range []struct{ next, cur *int64 }{
		{&p.PricePerStarMinor, &current.PricePerStarMinor},
		{&p.WritingPriceMinor, &current.WritingPriceMinor},
		{&p.SpeakingPriceMinor, &current.SpeakingPriceMinor},
	} {
		if *f.next == 0 {
			*f.next = *f.cur
		}
		if !validPrice(*f.next) {
			return Pricing{}, ErrInvalidPrice
		}
	}
	return s.repo.SetPricing(ctx, p, actor.AdminID)
}

// StarsAdmin is the platform administrator's pricing screen.
type StarsAdmin struct {
	Pricing           Pricing       `json:"pricing"`
	PaymentsAvailable bool          `json:"payments_available"`
	Provider          string        `json:"provider"`
	Purchases         []PurchaseRow `json:"purchases"`
}

func (s *Service) StarsAdmin(ctx context.Context, actor Actor) (StarsAdmin, error) {
	if !actor.platform() {
		return StarsAdmin{}, ErrForbidden
	}
	pricing, err := s.repo.Pricing(ctx)
	if err != nil {
		return StarsAdmin{}, err
	}
	rows, err := s.repo.RecentPurchases(ctx, 50)
	if err != nil {
		return StarsAdmin{}, err
	}
	return StarsAdmin{Pricing: pricing, PaymentsAvailable: s.salesOpen(pricing), Provider: s.provider.Name(), Purchases: rows}, nil
}

// Purchases nobody has paid for within this long are closed as expired.
const purchaseExpiry = 48 * time.Hour

// Sweep settles pending purchases from the provider's records: payments whose
// callback never arrived are credited, failed ones closed, and checkouts left
// unpaid for 48 hours expired. It returns how many purchases changed.
func (s *Service) Sweep(ctx context.Context, now time.Time) int {
	pending, err := s.repo.PendingPurchases(ctx, 100)
	if err != nil {
		return 0
	}
	changed := 0
	for _, p := range pending {
		if ctx.Err() != nil {
			break
		}
		if after := s.refresh(ctx, p); after.Status != p.Status {
			changed++
			continue
		}
		if now.Sub(p.CreatedAt) > purchaseExpiry {
			if _, err := s.repo.SettleFailed(ctx, p.ID, "expired: no payment received"); err == nil {
				changed++
			}
		}
	}
	return changed
}

// Run sweeps every few minutes until ctx ends.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Sweep(ctx, time.Now())
		}
	}
}
