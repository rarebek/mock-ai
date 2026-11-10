package telegrambot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RunResults sends students their results in Telegram.
//
// Every few seconds it finds results produced since the student connected
// (scored Reading/Listening attempts, completed Writing and Speaking
// reports), records each once in telegram_deliveries, and sends them. Nothing
// in the exam, Writing or Speaking code has to call it, so results from full
// mocks and practice alike are covered, and a restart never loses one.
func (b *Bot) RunResults(ctx context.Context, pool *pgxpool.Pool) {
	for ctx.Err() == nil {
		if err := discoverResults(ctx, pool); err != nil && ctx.Err() == nil {
			b.logger.Error("discover results for Telegram", "error", err)
		}
		if err := b.deliverResults(ctx, pool); err != nil && ctx.Err() == nil {
			b.logger.Error("deliver results to Telegram", "error", err)
		}
		if !wait(ctx, 10*time.Second) {
			return
		}
	}
}

func discoverResults(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `INSERT INTO telegram_deliveries (kind, item_id, user_id)
		SELECT 'exam', a.id, a.user_id FROM attempts a
			JOIN telegram_preferences p ON p.user_id = a.user_id AND p.results_enabled
			WHERE a.status = 'scored' AND a.scored_at >= p.chat_started_at AND a.scored_at > now() - interval '3 days'
		UNION ALL
		SELECT 'writing', w.id, w.user_id FROM writing_submissions w
			JOIN telegram_preferences p ON p.user_id = w.user_id AND p.results_enabled
			WHERE w.status = 'completed' AND w.report IS NOT NULL AND w.updated_at >= p.chat_started_at AND w.updated_at > now() - interval '3 days'
		UNION ALL
		SELECT 'speaking', s.id, s.user_id FROM speaking_sessions s
			JOIN telegram_preferences p ON p.user_id = s.user_id AND p.results_enabled
			WHERE s.status = 'completed' AND s.report IS NOT NULL AND s.updated_at >= p.chat_started_at AND s.updated_at > now() - interval '3 days'
		ON CONFLICT (kind, item_id) DO NOTHING`)
	return err
}

type delivery struct {
	kind, itemID, userID string
	attempts             int
}

const maxDeliveryAttempts = 8

func (b *Bot) deliverResults(ctx context.Context, pool *pgxpool.Pool) error {
	// Claim with a lease: a crash mid-send retries after five minutes.
	rows, err := pool.Query(ctx, `WITH due AS (
			SELECT kind, item_id FROM telegram_deliveries
			WHERE status = 'pending' AND next_attempt_at <= now()
			ORDER BY next_attempt_at LIMIT 20 FOR UPDATE SKIP LOCKED)
		UPDATE telegram_deliveries d SET attempts = d.attempts + 1, next_attempt_at = now() + interval '5 minutes'
		FROM due WHERE d.kind = due.kind AND d.item_id = due.item_id
		RETURNING d.kind, d.item_id, d.user_id, d.attempts`)
	if err != nil {
		return err
	}
	var batch []delivery
	for rows.Next() {
		var d delivery
		if err := rows.Scan(&d.kind, &d.itemID, &d.userID, &d.attempts); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range batch {
		b.deliver(ctx, pool, d)
	}
	return nil
}

func (b *Bot) deliver(ctx context.Context, pool *pgxpool.Pool, d delivery) {
	mark := func(status, lastError string, retryIn time.Duration) {
		if _, err := pool.Exec(ctx, `UPDATE telegram_deliveries SET status = $3, last_error = NULLIF($4, ''),
			sent_at = CASE WHEN $3 = 'sent' THEN now() ELSE sent_at END,
			next_attempt_at = now() + make_interval(secs => $5)
			WHERE kind = $1 AND item_id = $2`, d.kind, d.itemID, status, lastError, retryIn.Seconds()); err != nil {
			b.logger.Warn("record Telegram delivery", "error", err)
		}
	}
	var subject string
	if err := pool.QueryRow(ctx, `SELECT provider_subject FROM user_identities WHERE user_id = $1 AND provider = 'telegram'`, d.userID).Scan(&subject); err != nil {
		mark("undeliverable", "no Telegram account linked", 0)
		return
	}
	chatID, err := strconv.ParseInt(subject, 10, 64)
	if err != nil {
		mark("undeliverable", "invalid Telegram id", 0)
		return
	}
	// No study profile (or a failed read) only costs the 🎯 line and effect.
	var target float64
	_ = pool.QueryRow(ctx, `SELECT target_band::float8 FROM student_profiles WHERE user_id = $1`, d.userID).Scan(&target)
	body, path, band, err := b.composeResult(ctx, pool, d, target)
	if err != nil {
		mark("undeliverable", "result unavailable: "+err.Error(), 0)
		return
	}
	effect := ""
	if reachedTarget(band, target) {
		effect = celebrateEffect
	}
	err = b.sendRich(ctx, chatID, body, [][]any{{
		map[string]any{"text": "Open full review", "style": "success", "web_app": map[string]string{"url": b.miniAppURL + path}},
	}}, effect)
	var apiErr *APIError
	switch {
	case err == nil:
		mark("sent", "", 0)
	case errors.As(err, &apiErr) && (apiErr.Code == 403 || apiErr.Code == 400):
		// Blocked, never started, or a message Telegram will never accept:
		// retrying cannot help.
		mark("undeliverable", err.Error(), 0)
	case d.attempts >= maxDeliveryAttempts:
		mark("failed", err.Error(), 0)
	default:
		mark("pending", err.Error(), time.Duration(d.attempts*d.attempts)*time.Minute)
	}
}

func (b *Bot) composeResult(ctx context.Context, pool *pgxpool.Pool, d delivery, target float64) (string, string, float64, error) {
	switch d.kind {
	case "exam":
		var r examResult
		if err := pool.QueryRow(ctx, `SELECT ev.title, ev.skill::text, a.band_score::float8, a.raw_score::float8, a.question_count
			FROM attempts a JOIN exam_versions ev ON ev.id = a.exam_version_id WHERE a.id = $1`, d.itemID).
			Scan(&r.Title, &r.Skill, &r.Band, &r.Correct, &r.Total); err != nil {
			return "", "", 0, err
		}
		return formatExamResult(r, target), "/#review", r.Band, nil
	case "writing":
		var task string
		var report []byte
		if err := pool.QueryRow(ctx, `SELECT COALESCE(reference->>'task', ''), report FROM writing_submissions WHERE id = $1`, d.itemID).Scan(&task, &report); err != nil {
			return "", "", 0, err
		}
		text, band, err := formatReport("writing", writingTaskLabel(task), report, target)
		return text, "/#writing", band, err
	case "speaking":
		var topic string
		var report []byte
		if err := pool.QueryRow(ctx, `SELECT topic, report FROM speaking_sessions WHERE id = $1`, d.itemID).Scan(&topic, &report); err != nil {
			return "", "", 0, err
		}
		text, band, err := formatReport("speaking", topic, report, target)
		return text, "/#speaking", band, err
	}
	return "", "", 0, fmt.Errorf("unknown result kind %q", d.kind)
}

type examResult struct {
	Title   string
	Skill   string
	Band    float64
	Correct float64
	Total   int
}

func formatExamResult(r examResult, target float64) string {
	icon, skill := "📖", "Reading"
	if r.Skill == "listening" {
		icon, skill = "🎧", "Listening"
	}
	pct := 0
	if r.Total > 0 {
		pct = int(math.Round(r.Correct / float64(r.Total) * 100))
	}
	return fmt.Sprintf("<h3>%s %s result</h3><p>%s</p>%s<p>%s of %d correct (%d%%)</p><footer>Open the review to see every answer next to the correct one.</footer>",
		icon, skill, html.EscapeString(r.Title), bandBlock(r.Band, target), trimScore(r.Correct), r.Total, pct)
}

func reachedTarget(band, target float64) bool { return target > 0 && band >= target }

// bandBlock is the headline band, plus a 🎯 line once it meets the target.
func bandBlock(band, target float64) string {
	s := fmt.Sprintf("<h1>Band %.1f</h1>", band)
	if reachedTarget(band, target) {
		s += fmt.Sprintf("<p>🎯 You reached your Band %.1f target.</p>", target)
	}
	return s
}

func trimScore(v float64) string {
	if v == math.Trunc(v) {
		return strconv.Itoa(int(v))
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

func writingTaskLabel(task string) string {
	switch task {
	case "task_1":
		return "Academic Task 1"
	case "task_2":
		return "Task 2"
	}
	return ""
}

type reportSummary struct {
	Overall  *float64 `json:"overall"`
	Criteria []struct {
		Key   string   `json:"key"`
		Score *float64 `json:"score"`
	} `json:"criteria"`
	NextSteps []string `json:"next_steps"`
}

var criterionLabels = map[string]string{
	"task_response":                  "Task response",
	"task_achievement":               "Task achievement",
	"coherence_cohesion":             "Coherence and cohesion",
	"lexical_resource":               "Lexical resource",
	"grammatical_range":              "Grammar",
	"grammatical_accuracy":           "Grammar",
	"grammar":                        "Grammar",
	"fluency_coherence":              "Fluency and coherence",
	"fluency_and_coherence":          "Fluency and coherence",
	"pronunciation":                  "Pronunciation",
	"grammatical_range_and_accuracy": "Grammar",
}

func criterionLabel(key string) string {
	if l, ok := criterionLabels[key]; ok {
		return l
	}
	l := strings.ReplaceAll(key, "_", " ")
	if l == "" {
		return "Criterion"
	}
	return strings.ToUpper(l[:1]) + l[1:]
}

// formatReport summarises a Writing or Speaking report: overall band, a table
// of criteria, and the first next step. It returns the overall band.
func formatReport(kind, subject string, raw []byte, target float64) (string, float64, error) {
	var r reportSummary
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", 0, err
	}
	if r.Overall == nil {
		return "", 0, errors.New("report has no overall band")
	}
	icon, title := "✍️", "Writing report"
	if kind == "speaking" {
		icon, title = "🎤", "Speaking report"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "<h3>%s %s ready</h3>", icon, title)
	if subject != "" {
		fmt.Fprintf(&sb, "<p>%s</p>", html.EscapeString(subject))
	}
	sb.WriteString(bandBlock(*r.Overall, target))
	var rows strings.Builder
	for _, c := range r.Criteria {
		if c.Score != nil {
			fmt.Fprintf(&rows, "<tr><td>%s</td><td>%.1f</td></tr>", html.EscapeString(criterionLabel(c.Key)), *c.Score)
		}
	}
	if rows.Len() > 0 {
		fmt.Fprintf(&sb, "<table><tr><th>Criterion</th><th>Band</th></tr>%s</table>", rows.String())
	}
	if len(r.NextSteps) > 0 && strings.TrimSpace(r.NextSteps[0]) != "" {
		fmt.Fprintf(&sb, "<blockquote><b>Next step:</b> %s</blockquote>", html.EscapeString(strings.TrimSpace(r.NextSteps[0])))
	}
	sb.WriteString("<footer>AI estimate for practice, not an official IELTS score.</footer>")
	return sb.String(), *r.Overall, nil
}
