package application

// Package is a bundle of stars a student can buy.
//
// AmountMinor is in the currency's minor unit (UZS has 100 tiyin per sum, so
// 25 000 sum is 2_500_000). While prices are 0 the stars page shows the
// packages as "coming soon" and buying is refused, whatever the provider.
type Package struct {
	ID          string `json:"id"`
	Stars       int32  `json:"stars"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Label       string `json:"label"`
	Highlight   bool   `json:"highlight,omitempty"`
}

// DefaultPackages is the catalogue until prices are decided. Set AmountMinor
// on each package (and a real Provider) to open sales.
func DefaultPackages() []Package {
	return []Package{
		{ID: "stars-1", Stars: 1, Currency: "UZS", Label: "One full mock"},
		{ID: "stars-5", Stars: 5, Currency: "UZS", Label: "A month of weekly mocks", Highlight: true},
		{ID: "stars-12", Stars: 12, Currency: "UZS", Label: "Serious exam preparation"},
	}
}
