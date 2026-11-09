# Stars, practice credits and payments

Students pay with three balances:

| Balance | Pays for | Default price |
| --- | --- | --- |
| **Stars** | one full four-skill mock each | 30 000 so'm |
| **Writing checks** | one AI-evaluated essay in Practice | 10 000 so'm |
| **Speaking interviews** | one AI-assessed interview (live or recorded) in Practice | 10 000 so'm |

Every account starts with one of each. Reading and Listening practice are free.

| Rule | Where it lives |
| --- | --- |
| Every account starts with 1 star (credited the first time its balance is read) | `stars/application.Service` (welcome = 1 in `cmd/api/main.go`) |
| A full four-skill mock costs 1 star, charged once when it starts | `spendCost` in `stars/application/service.go` |
| Resuming an unfinished mock is free (the client sends one reference per mock; repeats are not charged) | `star_ledger` unique `(user_id, reason, reference)` |
| Practice and single-skill tests are free | not charged anywhere |
| A balance can never go negative; concurrent starts cannot both take the last star | `CHECK (balance >= 0)` + `SELECT … FOR UPDATE` in `stars/postgres` |
| Stars are credited only by the payment provider's server callback, once | `Repository.SettlePaid` |

### Practice credits (AI Writing and Speaking)

Writing evaluation (`POST /api/v1/writing/submissions/{id}/evaluate`) and Speaking interviews (`POST /api/v1/speaking/sessions`, `POST /api/v1/speaking/live/sessions`) call the AI, so the server charges for them through `credits.Charger` (`internal/platform/credits`, implemented by the stars service):

- **Writing**: one Writing check per essay (`writing:<submission id>`), so re-evaluating the same essay after a failure is free. Nothing is charged when the student's centre has AI marking off (a teacher marks it).
- **Speaking**: one Speaking interview per interview (`speaking:<session id>`). If it cannot be paid for, the just-created interview is deleted.
- **Inside a paid full mock** the client sends `full_mock: <mock id>`. The mock id is the reference its star was spent with; if that star was spent by this student within 48 hours, the mock covers up to 2 Writing evaluations (Task 1 and 2) and 2 Speaking interviews (one restart) at no charge (`star_mock_usage`). Beyond that, or for an unpaid id, practice credits are used.
- With none left the API answers **402** `{error: {code: "no_practice_credits", kind, message}}` and the app offers the Stars page.

### Learning centres

Students who belong to a learning centre (`users.centre_id`) do **not** use personal balances. Their centre holds shared balances of all three kinds (`star_centre_wallets`): each full mock a centre student starts takes one of the centre's stars, and each AI essay or interview in Practice takes one of the centre's Writing checks or Speaking interviews; the ledger row records the student and the centre, so the centre can see who used what. Centre students cannot buy stars (`403 centre_managed`); when the centre is empty they get `402 centre_out_of_stars` and are told to ask their centre.

Centres start at 0 stars. A centre's administrator buys stars online under "Your centre" (when sales are open), and a **platform administrator** can also add stars in the admin panel (Centres → a centre → Stars), with an optional note such as an invoice number. A centre's own administrator sees the balance and recent activity under "Your centre" but cannot add stars. `star_purchases.centre_id` and `SettlePaid` already credit a centre, so centre checkout can be added later without schema changes.

Every change is written to `star_ledger` with its reason (`welcome`, `full_mock`, `purchase`, `grant`, `refund`), so any balance can be explained.

## API

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/api/v1/stars` | `{source: "personal"\|"centre", centre_name, balance, full_mock_cost, price_per_star_minor, max_stars, packages, payments_available}` |
| POST | `/api/v1/stars/spend` | `{purpose: "full_mock", reference}` → `{balance}`; **402** `insufficient_stars` when empty |
| POST | `/api/v1/stars/purchases` | `{stars}` → `{purchase, checkout_url}`; **503** `payments_unavailable` while sales are closed; **502** `payment_provider_error` if inPAY refuses |
| GET | `/api/v1/stars/purchases/{id}` | the student's own purchase, for the after-payment page |
| GET | `/api/v1/admin/centres/{centre_id}/stars` | admin session; balance + last 50 changes; a centre admin only for their own centre |
| POST | `/api/v1/admin/centres/{centre_id}/stars/grants` | admin session, platform admins only; `{stars, reference, note}`; a repeated reference counts once |
| POST | `/api/v1/admin/centres/{centre_id}/stars/purchases` | the centre's own administrator; `{stars}` → `{purchase, checkout_url}` |
| GET | `/api/v1/admin/stars/purchases/{id}` | a centre purchase, for the panel's after-payment notice |
| GET | `/api/v1/admin/stars` | platform admins: pricing, whether sales are open, last 50 purchases |
| PUT | `/api/v1/admin/stars/pricing` | platform admins: `{price_per_star_minor}` |
| POST | `/api/v1/payments/{provider}/callback` | provider → server; no student session; only the configured provider answers |

After paying, the provider sends the student to `<WEB_ORIGIN>/?purchase=<id>#stars`. The page polls the purchase until the callback has settled it.

## Prices

One star = one full mock. A platform administrator sets its price in the admin panel (**Pricing** tab); it starts at **30 000 so'm** (`star_settings`, migration 26). Students see packages of 1, 5 and 10 stars priced from it; a centre administrator buys any number of stars (up to 5 000) at the same price. Each purchase stores the amount it was created with, so a price change never affects a payment already started.

Amounts are kept in minor units: for UZS **1 so'm = 100**, so 30 000 so'm is `3_000_000`. inPAY charges whole so'm with a minimum of 1 000, so the price must be a whole number of so'm between 1 000 and 10 000 000.

## inPAY

`internal/stars/payment/inpay` sells stars through inPAY API v2.

1. **Checkout**: `POST https://inpay.uz/api/v2/payments/` with `amount` (whole so'm), `return_url`, `webhook_url = <WEB_ORIGIN>/api/v1/payments/inpay/callback`, `metadata.purchase_id`, and `Idempotency-Key: mockai-<purchase id>`. The student (or centre administrator) is sent to the returned `checkout_url`.
2. **Webhook**: if `INPAY_WEBHOOK_SECRET` is set, `X-Inpay-Signature` is verified (HMAC-SHA256 of `"{t}.{raw body}"`, at most 5 minutes old). Either way the payment is then fetched with our key (`GET /payments/{id}`) and only that answer is trusted: it must belong to the same purchase and have the same amount. `succeeded` credits the stars once; `failed` / `canceled` closes the purchase.
3. **Return page**: when the buyer comes back, a pending purchase is checked against inPAY directly, so a slow webhook does not leave them waiting.
4. **Sweep**: every 3 minutes the API re-checks pending purchases (lost webhooks get credited) and expires checkouts left unpaid for 48 hours.

Students return to `<WEB_ORIGIN>/?purchase=<id>#stars`; centre administrators to `<ADMIN_ORIGIN>/?purchase=<id>`, which opens "Your centre".

Refunds are not available through inPAY's API; they are done manually in the inPAY cabinet or through their support.

### Configuration

| Setting | Where | Value |
| --- | --- | --- |
| `PAYMENT_PROVIDER` | GitHub repository **variable** | `inpay` to open sales; `none` (default) keeps them closed |
| `INPAY_SECRET_KEY` | GitHub **secret** | the `sk_live_…` key (never the test key in production) |
| `INPAY_WEBHOOK_SECRET` | GitHub **secret**, optional | `whsec_…` from `GET /api/v2/webhooks/secret/` |

The live key only works from IP addresses whitelisted in the inPAY cabinet (`/merchant/whitelist/`, "Kassa Strict" mode): the VPS address must be listed. The `return_url` domains (`mock-ai.uz`, `panel.mock-ai.uz`) must be attached to the merchant.

Local testing: run the API with `PAYMENT_PROVIDER=inpay INPAY_SECRET_KEY=sk_test_…`. Test checkout pages accept `8600 0000 0000 0001` (succeeds) and `…0002` (insufficient funds). Webhooks cannot reach localhost, but the return-page check credits the test payment.

## Connecting a payment provider

1. Create `internal/stars/payment/<name>/` with a type implementing `application.Provider` (the checklist is on the interface in `stars/application/provider.go`):
   - `Checkout` builds the provider's payment page for the purchase (amount = `Purchase.AmountMinor`, order id = `Purchase.ID`, return URL = `req.ReturnURL`).
   - `HandleCallback` verifies the provider's signature and amount, then calls `settle.MarkPaid` or `settle.MarkFailed`, and writes the response body the provider's protocol expects (Payme's JSON-RPC methods, Click's prepare/complete, Stripe's webhook, …).
2. Add a `case` for it in `internal/stars/payment/payment.go`, reading its credentials from the environment.
3. Set prices in `packages.go`.
4. In production set `PAYMENT_PROVIDER=<name>` and the credentials as GitHub secrets, passed into `api/.env` by `.github/workflows/backend.yml`.
5. In the provider's merchant cabinet, set the callback URL to `https://mock-ai.uz/api/v1/payments/<name>/callback`.

Nothing in the student app changes: once `payments_available` is true and a package has a price, "Get stars" sends the student to the provider's checkout.
