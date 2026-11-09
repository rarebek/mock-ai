# HTTP API

All request and response bodies use JSON. Authentication uses the `mock_ai_session` secure, HTTP-only cookie. Except for registration and login, application endpoints require that cookie.

## Identity

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/api/v1/auth/register` | Create an email/password student and session |
| `POST` | `/api/v1/auth/login` | Authenticate an email/password student |
| `POST` | `/api/v1/auth/telegram` | Verify Telegram Mini App `initData` and authenticate |
| `GET` | `/api/v1/auth/google/start` | Start Google Authorization Code + PKCE flow |
| `GET` | `/api/v1/auth/google/callback` | Verify Google callback and create a session |
| `POST` | `/api/v1/auth/logout` | Revoke the current session |
| `GET` | `/api/v1/me` | Return the current user and roles |

Google login automatically links to an existing account when Google returns the same verified email. Telegram uses the stable Telegram user ID as its identity subject.

When `TELEGRAM_BOT_TOKEN` is configured, the API also consumes bot updates. `/start` creates or refreshes the same Telegram-backed student profile, sends a first-time or returning-user greeting, and exposes the Mini App through inline and persistent menu buttons.

## Exams and attempts

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/v1/exams` | List published Listening and Reading exams |
| `GET` | `/api/v1/exams/{exam_id}` | Load published sections, material, questions, and options |
| `POST` | `/api/v1/exams/{exam_id}/attempts` | Start an attempt or resume its active attempt |
| `GET` | `/api/v1/attempts/{attempt_id}` | Restore an owned attempt and its saved answers |
| `PUT` | `/api/v1/attempts/{attempt_id}/answers/{question_id}` | Save an owned answer while the server timer is active |
| `POST` | `/api/v1/attempts/{attempt_id}/submit` | Lock and score an attempt transactionally |
| `GET` | `/api/v1/me/attempts` | Return paginated attempt history |
| `GET` | `/api/v1/me/progress` | Aggregate score, band, and accuracy statistics per skill |
| `GET` | `/api/v1/attempts/{attempt_id}/review` | Return owned scored answers, accepted answers, and explanations |
| `POST` | `/api/v1/attempts/{attempt_id}/reviewed` | Record an owned scored-attempt review as study activity |

## Focused practice

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/v1/practice/questions?skill=reading&type=multiple_choice&limit=5` | Build an untimed set from dedicated practice content |
| `POST` | `/api/v1/practice/questions/{question_id}/check` | Check one practice answer and reveal its accepted answer and explanation |

Practice checks accept the same `{"values":["answer"]}` shape as mock answers. Practice content is separated from scored mock content at the database level, so checking an answer cannot expose a mock-exam answer key. Practice checks are formative and do not affect band history.

## Learning tools and content

| Method | Path | Purpose |
| --- | --- | --- |
| GET / PUT | `/api/v1/attempts/{attempt_id}/workspace` | Read/save owned attempt flags, notes and passage highlights; writes require an active attempt |
| GET | `/api/v1/me/mistakes` | Latest 200 distinct missed questions from practice and scored mocks, with retry resolution state |
| POST | `/api/v1/me/mistakes/{question_id}/retry` | Check `{"values":["answer"]}` for a question previously missed by this student |
| GET / PUT | `/api/v1/me/reminder` | Read/update opt-in Telegram reminder preferences: enabled, local_time (`HH:MM`), timezone (IANA name) |
| GET / POST | `/api/v1/admin/content` | List drafts/create draft (separate admin session required) |
| PUT | `/api/v1/admin/content/{draft_id}` | Save edits to a draft (separate admin session required) |
| POST | `/api/v1/admin/content/{draft_id}/publish` | Validate and publish an immutable version; archive the prior published version |
| GET | `/api/v1/admin/content-imports` | List the signed-in admin's latest PDF imports and generated draft IDs |
| POST | `/api/v1/admin/content-imports` | Upload multipart `file`, `skills` and `rights_confirmed=true`; returns 202 |

Reminders default to disabled. The bot checks every minute, claims at most one delivery per local day, and links to `/#plan`. Claims prevent duplicate delivery across workers. Delivery is best effort: a transport failure after claiming can skip that day's reminder. No reminder is sent without opt-in and a linked Telegram identity.

Content supports multiple sections, options, accepted answers, explanations, HTTPS audio URLs and explicit mock band thresholds. Listening transcripts remain editor-only when published from the studio. Seeded practice recordings use synthetic narration of original scripts, labelled in the section instructions. The library is formative practice, not official IELTS exam material.

PDF imports are asynchronous and never publish automatically. Files are limited to 50 MB, verified as PDFs, and sent to Gemini Files for temporary processing. The worker inventories complete tests, extracts each into a separate editable draft, records a SHA-256 source fingerprint, and shows warnings to the importing administrator. Admins must verify passages, answers, explanations, and band conversions. A Listening book supplies questions and, where present, transcripts; the licensed audio URL must be added and previewed before publication. A processing job interrupted by an API restart is marked failed and can be uploaded again.

## Study planning endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/v1/me/study-plan` | Return study profile, streak, daily activity, seven-day activity, and a weak-area recommendation |
| `PUT` | `/api/v1/me/study-profile` | Update target band, exam date, IELTS track, and daily minute goal |

Study profile updates accept `target_band` in half-band steps from 4.0 to 9.0, an optional future `exam_date` in `YYYY-MM-DD` form, `track` as `academic` or `general_training`, and `daily_goal_minutes` from 10 to 180. General Training content is reserved by the API but is not yet exposed in the student UI.

Answer saves accept `{"values":["answer"]}`. Multiple-select questions use more than one value. Each successful overwrite increments the saved answer revision.
# Community and challenges

Authenticated `GET /api/v1/me/community` returns `profile` (nickname, enabled),
`stats` (questions, mocks, days, today_questions, week_mocks, week_days,
week_points), and `board` entries (nickname, points, position, is_me).
Authenticated `PUT /api/v1/me/community` accepts `{ "nickname": "Reader 42",
"enabled": true }` and returns 204. Nicknames allow 2–24 Unicode letters,
numbers, spaces, hyphens and underscores; invalid values return 422.

Participation defaults off. Only explicitly chosen nicknames and points are
visible to authenticated students; no public user identifiers, names, emails or
photos are returned. Disabling visibility removes the student immediately.
The board returns the top 50 plus the requesting student's position. Ties share
rank; there are no fabricated participants. Nicknames are not unique identities.

Points measure activity, not accuracy or IELTS ability: one point for each
distinct practice question and ten per distinct scored mock version with a
saved nonempty answer, each counted once per Monday–Sunday UTC week. Historical
activity in the current week counts on opt-in. Repeated submissions do not add
points. The current implementation is friendly competition, not a secure contest
or prize system. Points are derived from persisted activity, not client scores.

Daily five, three study days per week, and one answered mock per week power the
frontend challenges. Lifetime distinct questions, answered mock versions and
study days power achievements. Vocabulary warm-ups are local, unranked rounds.
Migration 000008 creates the opt-in table and aggregate views; rollback only
removes these community objects, preserving the underlying study records.
# Separate administrator identity

Admins are not students and are never created by assigning a student role.
`admins` stores their credentials; `admin_sessions` stores SHA-256 session-token
hashes. Legacy student roles grant no admin permissions. Admin email addresses
are unique within the admin realm; using the same email as a student does not
link the accounts or share credentials.

- `POST /api/v1/admin/auth/login`: `{ "email": "...", "password": "..." }`;
  returns `{ "admin": { "id", "email", "display_name" } }` and a separate
  HttpOnly, SameSite=Strict cookie scoped to `/api/v1/admin` (Secure in production).
- `GET /api/v1/admin/me`: current administrator, or 401.
- `POST /api/v1/admin/auth/logout`: revokes the current admin session, returns 204.

All admin writes, including login/logout, require an Origin matching
`ADMIN_ORIGIN`. Sessions expire after eight hours; disabled admins cannot use
existing sessions. Five failed login attempts lock an account for the remainder
of its 15-minute login window. There is no public administrator registration.
New drafts use `admin_author_id`; new exams use `created_by_admin`. Historical
system/student author references are retained solely as legacy metadata.
## Writing Task 2 (Gemini practice feedback)

All routes require a student session. Writes require an Origin equal to WEB_ORIGIN.

| Method | Route | Purpose |
| --- | --- | --- |
| GET | `/api/v1/writing/settings` | Availability, model ID and daily request limit; never returns keys |
| GET / POST | `/api/v1/writing/submissions` | Latest 50 owned essays / create a draft with prompt, essay and optional owned completed parent_id |
| GET / PUT / DELETE | `/api/v1/writing/submissions/{id}` | Owned record / update editable draft with revision / delete inactive essay and report |
| POST | `/api/v1/writing/submissions/{id}/evaluate` | Submit saved revision with `consent: true`; returns 202 and queued submission |

Evaluate requires 50–1800 words and a 20–4000-byte task prompt. Drafts can be
shorter. Task 2's practice target is 250+ words. Essay size is limited to 20 KB.
Queued/processing/completed submissions are immutable. Revisions are new drafts
linked to the original; original reports stay available. Deleting a parent clears
the link without deleting revisions. A stale draft revision returns 409.

Evaluation is a durable PostgreSQL queue, not an HTTP-long-running request.
Atomic per-user locking prevents concurrent duplicate billing requests and caps
evaluation requests, including retries, at five per UTC day. Deleting an essay
does not reset that quota. A worker claims one job at a time; its three-minute
lease and per-run identifier prevent stale completions from replacing a retry.
Expired jobs become failed rather than silently triggering another paid call.
Students may explicitly retry failed jobs. Poll the owned GET route for status.

Reports contain summary, strengths, four criterion estimates, quoted issues
(including argument development), a sample passage revision and prioritized
next steps. Quotes are validated as exact essay substrings. Malformed or incomplete
feedback is failed, not presented as a score. The backend computes `overall` as
the equal criterion average rounded to the nearest half band. This is a Task 2
practice estimate, not a combined Task 1/2 Writing score or overall IELTS score.
Model version and rubric version are recorded for reproducibility.

Only the task prompt and essay are sent to Google Gemini, after explicit consent.
No user identifier, email, Telegram profile, secret or other student's work enters
the prompt. AI output is rendered as text, never executable HTML. This does not
provide plagiarism detection, fact checking or an official examiner assessment.

## Speaking

Live streamed three-part interviews, private audio playback,
and AI Speaking reports are available under `/api/v1/speaking`. See
[Speaking HTTP contract, retention and verification](speaking.md).
New sessions use `POST /api/v1/speaking/live/sessions` and the authenticated,
same-origin WebSocket `/api/v1/speaking/live/sessions/{id}/stream`. Earlier recorded
interviews retain their original endpoints for compatibility.

## Stars

Balance, spending and purchases: see [payments.md](payments.md).

## Telegram results

Students can receive their results from the Telegram bot.

- `GET /api/v1/me/telegram` → `{linked, started, results_enabled}`. `started` means the student has pressed Start in the bot, so it can message them.
- `POST /api/v1/me/telegram/link` → `{token, start, expires_at}`. The app opens `https://t.me/<bot>?start=<start>` (`l_<token>`, valid 30 minutes, single use). Pressing Start attaches that Telegram account to the student's existing account; a Telegram account already used by another mock-ai account is refused.
- `PUT /api/v1/me/telegram/results` `{enabled}` switches result messages on or off.

The bot's results worker (`internal/telegrambot/results.go`) checks every 10 seconds for results produced after the student connected — scored Reading/Listening attempts and completed Writing/Speaking reports — records each once in `telegram_deliveries`, and sends a summary with an "Open full review" button. A student who blocked the bot is marked `undeliverable`; other failures retry with backoff, up to 8 attempts.
