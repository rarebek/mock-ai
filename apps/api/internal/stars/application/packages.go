package application

import (
	"fmt"
	"time"
)

// Pricing is the price of one star (one full mock), set by a platform
// administrator. AmountMinor values are in the currency's minor unit: UZS has
// 100 tiyin per so'm, so 30 000 so'm is 3_000_000.
type Pricing struct {
	PricePerStarMinor int64     `json:"price_per_star_minor"`
	Currency          string    `json:"currency"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// Limits on what one purchase may contain and on the price itself.
const (
	MaxPersonalStars int32 = 50
	MaxCentreStars   int32 = 5000
	// inpay charges whole so'm with a minimum of 1 000.
	minPriceMinor int64 = 1_000_00
	maxPriceMinor int64 = 10_000_000_00
)

// Package is a quick pick on a student's stars page, priced from Pricing.
type Package struct {
	ID          string `json:"id"`
	Stars       int32  `json:"stars"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Label       string `json:"label"`
	Highlight   bool   `json:"highlight,omitempty"`
}

var personalOffers = []struct {
	stars     int32
	label     string
	highlight bool
}{
	{1, "One full mock", false},
	{5, "A month of weekly mocks", true},
	{10, "Serious exam preparation", false},
}

func packagesFor(p Pricing) []Package {
	out := make([]Package, 0, len(personalOffers))
	for _, o := range personalOffers {
		out = append(out, Package{ID: packageID(o.stars), Stars: o.stars, AmountMinor: int64(o.stars) * p.PricePerStarMinor, Currency: p.Currency, Label: o.label, Highlight: o.highlight})
	}
	return out
}

func packageID(stars int32) string { return fmt.Sprintf("stars-%d", stars) }

// ParsePackageID accepts the "stars-N" ids older clients send.
func ParsePackageID(id string) (int32, bool) {
	var n int32
	if _, err := fmt.Sscanf(id, "stars-%d", &n); err != nil || packageID(n) != id {
		return 0, false
	}
	return n, true
}
