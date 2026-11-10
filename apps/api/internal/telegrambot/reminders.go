package telegrambot

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/own/mock-ai/apps/api/internal/platform/postgres/sqlc"
	"strconv"
	"time"
)

// RunReminders claims one delivery per student's local day. Preferences are
// disabled by default and are enabled explicitly in the student profile.
func (b *Bot) RunReminders(ctx context.Context, pool *pgxpool.Pool) {
	q := db.New(pool)
	for ctx.Err() == nil {
		rows, err := q.ClaimReminders(ctx)
		if err != nil && ctx.Err() == nil {
			b.logger.Error("claim study reminders", "error", err)
		}
		for _, row := range rows {
			chatID, err := strconv.ParseInt(row.ProviderSubject, 10, 64)
			if err != nil {
				continue
			}
			body := "<h3>📚 Your study time is here</h3><p>A short practice session is a good next step. Open your study plan to continue.</p>"
			// tg-time "r" shows the countdown in the student's own language, e.g. "in 3 weeks".
			var examUnix int64
			var examDay string
			if pool.QueryRow(ctx, `SELECT extract(epoch FROM exam_date)::bigint, to_char(exam_date, 'FMMonth FMDD') FROM student_profiles
				WHERE user_id = $1 AND exam_date > current_date`, row.UserID).Scan(&examUnix, &examDay) == nil {
				body += fmt.Sprintf(`<p>🗓 Your IELTS exam is <tg-time unix="%d" format="r">on %s</tg-time>.</p>`, examUnix, examDay)
			}
			body += "<footer>You can change or disable reminders in your profile.</footer>"
			err = b.sendRich(ctx, chatID, body, [][]any{{map[string]any{"text": "Open my study plan", "style": "primary", "web_app": map[string]string{"url": b.miniAppURL + "/#plan"}}}}, "")
			if err != nil && ctx.Err() == nil {
				b.logger.Warn("study reminder delivery failed", "error", err)
			}
		}
		if !wait(ctx, time.Minute) {
			return
		}
	}
}
