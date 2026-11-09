// Package credits is the contract between features that call the AI
// (Writing evaluation, Speaking interviews) and the stars service that pays
// for them. Features depend on this package only, never on stars directly.
package credits

import (
	"context"
	"errors"
	"strings"
)

// Kind is what a practice credit buys.
type Kind string

const (
	Writing  Kind = "writing"  // one AI-evaluated essay
	Speaking Kind = "speaking" // one AI-assessed interview
)

// ErrNoCredits means the student (or their learning centre) has none of this
// kind left. Errors returned by Charge wrap it with a message fit to show.
var ErrNoCredits = errors.New("no practice credits left")

// Charger takes one practice credit for an AI call.
type Charger interface {
	// Charge pays for item (e.g. "writing:<submission id>") once: repeating
	// the same item never charges again. When fullMock names a full mock the
	// student paid a star for, the mock's own allowance is used instead and
	// nothing is charged.
	Charge(ctx context.Context, userID string, kind Kind, item, fullMock string) error
}

// Message is the part of a Charge error meant for the student.
func Message(err error) string {
	s, prefix := err.Error(), ErrNoCredits.Error()+": "
	if i := strings.Index(s, prefix); i >= 0 {
		return s[i+len(prefix):]
	}
	return s
}
