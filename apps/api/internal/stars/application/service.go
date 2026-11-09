// Package application holds the rules for stars: the unit a student spends to
// sit a full mock exam, and buys in packages.
//
// The service never talks to a payment company directly. It talks to a
// Provider (see provider.go), so adding Payme, Click, Uzum or Stripe means
// writing one Provider and selecting it in configuration; nothing here
// changes.
package application

import (
	"context"
	"errors"
	"regexp"
	"time"
)

var (
	ErrInsufficient   = errors.New("you have no stars left; get more stars to start a full mock")
	ErrUnknownPackage = errors.New("that star package does not exist")
	ErrUnknownPurpose = errors.New("stars cannot be spent on that")
	ErrInvalidRef     = errors.New("a spend needs a client reference of 8–64 letters, digits, dashes or underscores")
	ErrNotFound       = errors.New("purchase not found")
	ErrPaymentsOff    = errors.New("payments are coming soon; stars cannot be bought yet")
	// A learning centre's students spend the centre's stars, not their own.
	ErrCentreInsufficient = errors.New("your learning centre has no stars left; ask your centre to add more")
	ErrCentreManaged      = errors.New("your learning centre provides your stars, so they cannot be bought here")
	ErrForbidden          = errors.New("you cannot manage this centre's stars")
	ErrInvalidGrant       = errors.New("add between 1 and 10000 stars, with a note of at most 200 characters")
)

// Reasons recorded in the ledger. They match the database CHECK constraint.
const (
	ReasonWelcome  = "welcome"
	ReasonFullMock = "full_mock"
	ReasonPurchase = "purchase"
	ReasonGrant    = "grant"
	ReasonRefund   = "refund"
)

// What a star can be spent on, and how many stars each costs.
var spendCost = map[string]int32{
	ReasonFullMock: 1,
}

type Wallet struct {
	Balance int32 `json:"balance"`
}

type Purchase struct {
	ID                string     `json:"id"`
	UserID            string     `json:"-"`
	PackageID         string     `json:"package_id"`
	Stars             int32      `json:"stars"`
	AmountMinor       int64      `json:"amount_minor"`
	Currency          string     `json:"currency"`
	Provider          string     `json:"provider"`
	ProviderReference string     `json:"-"`
	CentreID          string     `json:"-"`
	Status            string     `json:"status"`
	FailureReason     string     `json:"failure_reason,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	SettledAt         *time.Time `json:"settled_at,omitempty"`
}

// Repository is the storage the service needs. Every method that changes a
// balance runs in a single transaction and is idempotent on its reference.
type Repository interface {
	// EnsureWallet creates the wallet with the welcome stars the first time a
	// user is seen and returns the current balance.
	EnsureWallet(ctx context.Context, userID string, welcome int32) (int32, error)
	// Spend takes cost stars once per (user, reason, reference). A repeat of
	// the same reference returns the balance without charging again. Returns
	// ErrInsufficient when the balance is too low.
	Spend(ctx context.Context, userID, reason, reference string, cost int32) (int32, error)
	CreatePurchase(ctx context.Context, p Purchase) (Purchase, error)
	SetProviderReference(ctx context.Context, purchaseID, reference string) error
	Purchase(ctx context.Context, purchaseID string) (Purchase, error)
	// SettlePaid marks a pending purchase paid and credits its stars, once.
	SettlePaid(ctx context.Context, purchaseID, providerReference string) (Purchase, error)
	SettleFailed(ctx context.Context, purchaseID, reason string) (Purchase, error)

	// StudentCentre reports the learning centre a student belongs to, if any.
	StudentCentre(ctx context.Context, userID string) (Centre, bool, error)
	EnsureCentreWallet(ctx context.Context, centreID string) (int32, error)
	// SpendFromCentre takes cost stars from the centre once per (student,
	// reason, reference) and records which student used them. Returns
	// ErrCentreInsufficient when the centre's balance is too low.
	SpendFromCentre(ctx context.Context, centreID, userID, reason, reference string, cost int32) (int32, error)
	// GrantCentre adds stars to a centre once per reference.
	GrantCentre(ctx context.Context, centreID, adminID string, stars int32, reference, note string) (int32, error)
	CentreActivity(ctx context.Context, centreID string, limit int) ([]LedgerEntry, error)
}

// Centre is the learning centre a student belongs to.
type Centre struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// LedgerEntry is one change to a centre's balance, for its administrators.
type LedgerEntry struct {
	ID           string    `json:"id"`
	Reason       string    `json:"reason"`
	Delta        int32     `json:"delta"`
	BalanceAfter int32     `json:"balance_after"`
	StudentName  string    `json:"student_name,omitempty"`
	Note         string    `json:"note,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type Service struct {
	repo       Repository
	provider   Provider
	packages   []Package
	welcome    int32
	returnBase string
}

// New builds the service. returnBase is the student app's origin; after
// paying, the provider sends the student to <returnBase>/?purchase=<id>#stars.
func New(repo Repository, provider Provider, packages []Package, welcome int32, returnBase string) *Service {
	if provider == nil {
		provider = Unavailable{}
	}
	if welcome < 0 {
		welcome = 0
	}
	return &Service{repo: repo, provider: provider, packages: packages, welcome: welcome, returnBase: returnBase}
}

// Overview is everything the stars page needs in one request.
//
// Source is "personal" for a self-serve student and "centre" for a learning
// centre's student, whose balance is the centre's and who cannot buy stars.
type Overview struct {
	Source            string    `json:"source"`
	CentreName        string    `json:"centre_name,omitempty"`
	Balance           int32     `json:"balance"`
	FullMockCost      int32     `json:"full_mock_cost"`
	Packages          []Package `json:"packages"`
	PaymentsAvailable bool      `json:"payments_available"`
}

func (s *Service) Overview(ctx context.Context, userID string) (Overview, error) {
	centre, inCentre, err := s.repo.StudentCentre(ctx, userID)
	if err != nil {
		return Overview{}, err
	}
	if inCentre {
		balance, err := s.repo.EnsureCentreWallet(ctx, centre.ID)
		if err != nil {
			return Overview{}, err
		}
		return Overview{Source: "centre", CentreName: centre.Name, Balance: balance, FullMockCost: spendCost[ReasonFullMock], Packages: []Package{}}, nil
	}
	balance, err := s.repo.EnsureWallet(ctx, userID, s.welcome)
	if err != nil {
		return Overview{}, err
	}
	return Overview{
		Source:            "personal",
		Balance:           balance,
		FullMockCost:      spendCost[ReasonFullMock],
		Packages:          s.packages,
		PaymentsAvailable: s.provider.Available(),
	}, nil
}

var referencePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// Spend charges for something a student is about to start. reference is
// chosen by the client (one per full mock), so a retried request after a
// dropped connection cannot charge twice.
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
		balance, err := s.repo.SpendFromCentre(ctx, centre.ID, userID, purpose, reference, cost)
		if err != nil {
			return Wallet{}, err
		}
		return Wallet{Balance: balance}, nil
	}
	if _, err := s.repo.EnsureWallet(ctx, userID, s.welcome); err != nil {
		return Wallet{}, err
	}
	balance, err := s.repo.Spend(ctx, userID, purpose, reference, cost)
	if err != nil {
		return Wallet{}, err
	}
	return Wallet{Balance: balance}, nil
}

// CheckoutResult tells the client where to send the student to pay.
type CheckoutResult struct {
	Purchase    Purchase `json:"purchase"`
	CheckoutURL string   `json:"checkout_url"`
}

// StartPurchase records a pending purchase and asks the provider for a
// checkout page. Stars are credited later, by the provider's callback.
func (s *Service) StartPurchase(ctx context.Context, userID, packageID string) (CheckoutResult, error) {
	if _, inCentre, err := s.repo.StudentCentre(ctx, userID); err != nil {
		return CheckoutResult{}, err
	} else if inCentre {
		return CheckoutResult{}, ErrCentreManaged
	}
	pkg, ok := s.findPackage(packageID)
	if !ok {
		return CheckoutResult{}, ErrUnknownPackage
	}
	if !s.provider.Available() || pkg.AmountMinor <= 0 {
		return CheckoutResult{}, ErrPaymentsOff
	}
	purchase, err := s.repo.CreatePurchase(ctx, Purchase{
		UserID: userID, PackageID: pkg.ID, Stars: pkg.Stars,
		AmountMinor: pkg.AmountMinor, Currency: pkg.Currency, Provider: s.provider.Name(),
	})
	if err != nil {
		return CheckoutResult{}, err
	}
	returnURL := s.returnBase + "/?purchase=" + purchase.ID + "#stars"
	checkout, err := s.provider.Checkout(ctx, CheckoutRequest{Purchase: purchase, ReturnURL: returnURL})
	if err != nil {
		_, _ = s.repo.SettleFailed(ctx, purchase.ID, "checkout could not be created")
		return CheckoutResult{}, err
	}
	if checkout.Reference != "" {
		if err := s.repo.SetProviderReference(ctx, purchase.ID, checkout.Reference); err != nil {
			return CheckoutResult{}, err
		}
	}
	return CheckoutResult{Purchase: purchase, CheckoutURL: checkout.URL}, nil
}

// Purchase returns one of the student's own purchases (for the return page).
func (s *Service) Purchase(ctx context.Context, userID, purchaseID string) (Purchase, error) {
	p, err := s.repo.Purchase(ctx, purchaseID)
	if err != nil {
		return Purchase{}, err
	}
	if p.UserID != userID {
		return Purchase{}, ErrNotFound
	}
	return p, nil
}

func (s *Service) findPackage(id string) (Package, bool) {
	for _, p := range s.packages {
		if p.ID == id {
			return p, true
		}
	}
	return Package{}, false
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

// Actor is an administrator acting on a centre's stars. CentreID is empty for
// a platform administrator, who may manage every centre; a centre's own
// administrator may only see their centre.
type Actor struct {
	AdminID  string
	CentreID string
}

func (a Actor) platform() bool { return a.CentreID == "" }

// CentreStars is a centre's balance and its recent activity.
type CentreStars struct {
	CentreID     string        `json:"centre_id"`
	Balance      int32         `json:"balance"`
	FullMockCost int32         `json:"full_mock_cost"`
	CanGrant     bool          `json:"can_grant"`
	Activity     []LedgerEntry `json:"activity"`
}

func (s *Service) CentreStars(ctx context.Context, actor Actor, centreID string) (CentreStars, error) {
	if !actor.platform() && actor.CentreID != centreID {
		return CentreStars{}, ErrForbidden
	}
	balance, err := s.repo.EnsureCentreWallet(ctx, centreID)
	if err != nil {
		return CentreStars{}, err
	}
	activity, err := s.repo.CentreActivity(ctx, centreID, 50)
	if err != nil {
		return CentreStars{}, err
	}
	return CentreStars{CentreID: centreID, Balance: balance, FullMockCost: spendCost[ReasonFullMock], CanGrant: actor.platform(), Activity: activity}, nil
}

// GrantCentreStars tops up a centre. Until centres can pay online, this is
// how a platform administrator records stars a centre has bought.
func (s *Service) GrantCentreStars(ctx context.Context, actor Actor, centreID string, stars int32, reference, note string) (CentreStars, error) {
	if !actor.platform() {
		return CentreStars{}, ErrForbidden
	}
	if stars < 1 || stars > 10000 || len([]rune(note)) > 200 {
		return CentreStars{}, ErrInvalidGrant
	}
	if !referencePattern.MatchString(reference) {
		return CentreStars{}, ErrInvalidRef
	}
	if _, err := s.repo.EnsureCentreWallet(ctx, centreID); err != nil {
		return CentreStars{}, err
	}
	if _, err := s.repo.GrantCentre(ctx, centreID, actor.AdminID, stars, reference, note); err != nil {
		return CentreStars{}, err
	}
	return s.CentreStars(ctx, actor, centreID)
}
