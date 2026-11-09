# Stars and payments

Students spend **stars** to sit full mock exams.

| Rule | Where it lives |
| --- | --- |
| Every account starts with 1 star (credited the first time its balance is read) | `stars/application.Service` (welcome = 1 in `cmd/api/main.go`) |
| A full four-skill mock costs 1 star, charged once when it starts | `spendCost` in `stars/application/service.go` |
| Resuming an unfinished mock is free (the client sends one reference per mock; repeats are not charged) | `star_ledger` unique `(user_id, reason, reference)` |
| Practice and single-skill tests are free | not charged anywhere |
| A balance can never go negative; concurrent starts cannot both take the last star | `CHECK (balance >= 0)` + `SELECT … FOR UPDATE` in `stars/postgres` |
| Stars are credited only by the payment provider's server callback, once | `Repository.SettlePaid` |

Every change is written to `star_ledger` with its reason (`welcome`, `full_mock`, `purchase`, `grant`, `refund`), so any balance can be explained.

## API

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/api/v1/stars` | `{balance, full_mock_cost, packages, payments_available}` |
| POST | `/api/v1/stars/spend` | `{purpose: "full_mock", reference}` → `{balance}`; **402** `insufficient_stars` when empty |
| POST | `/api/v1/stars/purchases` | `{package_id}` → `{purchase, checkout_url}`; **503** `payments_unavailable` while sales are closed |
| GET | `/api/v1/stars/purchases/{id}` | the student's own purchase, for the after-payment page |
| POST | `/api/v1/payments/{provider}/callback` | provider → server; no student session; only the configured provider answers |

After paying, the provider sends the student to `<WEB_ORIGIN>/?purchase=<id>#stars`. The page polls the purchase until the callback has settled it.

## Prices

Packages are in `stars/application/packages.go` (`stars-1`, `stars-5`, `stars-12`). `AmountMinor` is in the currency's minor unit: for UZS, **1 so'm = 100**, so 40 000 so'm is `4_000_000`. While a package's price is 0 it shows "Price coming soon" and cannot be bought, even with a provider configured.

## Connecting a payment provider

1. Create `internal/stars/payment/<name>/` with a type implementing `application.Provider` (the checklist is on the interface in `stars/application/provider.go`):
   - `Checkout` builds the provider's payment page for the purchase (amount = `Purchase.AmountMinor`, order id = `Purchase.ID`, return URL = `req.ReturnURL`).
   - `HandleCallback` verifies the provider's signature and amount, then calls `settle.MarkPaid` or `settle.MarkFailed`, and writes the response body the provider's protocol expects (Payme's JSON-RPC methods, Click's prepare/complete, Stripe's webhook, …).
2. Add a `case` for it in `internal/stars/payment/payment.go`, reading its credentials from the environment.
3. Set prices in `packages.go`.
4. In production set `PAYMENT_PROVIDER=<name>` and the credentials as GitHub secrets, passed into `api/.env` by `.github/workflows/backend.yml`.
5. In the provider's merchant cabinet, set the callback URL to `https://mock-ai.uz/api/v1/payments/<name>/callback`.

Nothing in the student app changes: once `payments_available` is true and a package has a price, "Get stars" sends the student to the provider's checkout.
