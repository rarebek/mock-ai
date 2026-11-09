package application

import (
	"fmt"
	"time"
)

// Kind is what a credit buys.
type Kind string

const (
	KindStar     Kind = "star"     // one full mock
	KindWriting  Kind = "writing"  // one AI-evaluated essay in Practice
	KindSpeaking Kind = "speaking" // one AI-assessed interview in Practice
)

func validKind(k Kind) bool { return k == KindStar || k == KindWriting || k == KindSpeaking }

// Balances is how many of each kind a student or centre holds.
type Balances struct {
	Star     int32 `json:"star"`
	Writing  int32 `json:"writing"`
	Speaking int32 `json:"speaking"`
}

func (b Balances) of(k Kind) int32 {
	switch k {
	case KindWriting:
		return b.Writing
	case KindSpeaking:
		return b.Speaking
	}
	return b.Star
}

// Pricing is set by a platform administrator. Amounts are in the currency's
// minor unit: UZS has 100 tiyin per so'm, so 30 000 so'm is 3_000_000.
type Pricing struct {
	PricePerStarMinor  int64     `json:"price_per_star_minor"`
	WritingPriceMinor  int64     `json:"writing_price_minor"`
	SpeakingPriceMinor int64     `json:"speaking_price_minor"`
	Currency           string    `json:"currency"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func (p Pricing) of(k Kind) int64 {
	switch k {
	case KindWriting:
		return p.WritingPriceMinor
	case KindSpeaking:
		return p.SpeakingPriceMinor
	}
	return p.PricePerStarMinor
}

// Limits on what one purchase may contain and on prices.
const (
	MaxPersonalStars int32 = 50
	MaxCentreStars   int32 = 5000
	// inPAY charges whole so'm with a minimum of 1 000.
	minPriceMinor int64 = 1_000_00
	maxPriceMinor int64 = 10_000_000_00
)

func validPrice(minor int64) bool {
	return minor >= minPriceMinor && minor <= maxPriceMinor && minor%100 == 0
}

// Package is a quick pick on a student's stars page, priced from Pricing.
type Package struct {
	ID          string `json:"id"`
	Kind        Kind   `json:"kind"`
	Stars       int32  `json:"stars"` // how many of Kind
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Label       string `json:"label"`
	Highlight   bool   `json:"highlight,omitempty"`
}

var offers = []struct {
	kind      Kind
	count     int32
	label     string
	highlight bool
}{
	{KindStar, 1, "One full mock", false},
	{KindStar, 5, "A month of weekly mocks", true},
	{KindStar, 10, "Serious exam preparation", false},
	{KindWriting, 1, "One essay checked", false},
	{KindWriting, 5, "A week of essays", true},
	{KindWriting, 10, "Ten essays checked", false},
	{KindSpeaking, 1, "One interview", false},
	{KindSpeaking, 5, "A week of speaking", true},
	{KindSpeaking, 10, "Ten interviews", false},
}

func packagesFor(p Pricing) []Package {
	out := make([]Package, 0, len(offers))
	for _, o := range offers {
		out = append(out, Package{ID: packageID(o.kind, o.count), Kind: o.kind, Stars: o.count,
			AmountMinor: int64(o.count) * p.of(o.kind), Currency: p.Currency, Label: o.label, Highlight: o.highlight})
	}
	return out
}

func packageID(kind Kind, count int32) string {
	if kind == KindStar {
		return fmt.Sprintf("stars-%d", count)
	}
	return fmt.Sprintf("%s-%d", kind, count)
}

// ParsePackageID accepts the "stars-N" ids older clients send.
func ParsePackageID(id string) (int32, bool) {
	var n int32
	if _, err := fmt.Sscanf(id, "stars-%d", &n); err != nil || packageID(KindStar, n) != id {
		return 0, false
	}
	return n, true
}
