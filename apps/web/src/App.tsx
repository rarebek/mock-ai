import { ReactNode, useCallback, useEffect, useRef, useState } from "react";
import AuthScreen from "./AuthScreen";
import ExamRunner from "./ExamRunner";
import { APIError, authAPI, User } from "./auth";
import { Attempt, Exam, ExamSummary, examAPI, HistoryItem, SkillProgress } from "./exams";
import {
  ArrowRightIcon, Brand, ChartIcon, ChevronDownIcon, ClockIcon, ConfirmDialog, ErrorAlert, HomeIcon,
  InboxIcon, initialsOf, ListIcon, LogoIcon, LogoutIcon, SkillIcon, TargetIcon, TrophyIcon, BookIcon
} from "./ui";

function App() {
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);
  const [runner, setRunner] = useState<{ exam: Exam; attempt: Attempt } | null>(null);

  useEffect(() => {
    let active = true;
    async function restoreSession() {
      try {
        const current = await authAPI.me();
        if (active) setUser(current);
      } catch (error) {
        const initData = window.Telegram?.WebApp?.initData;
        if (initData && error instanceof APIError && error.status === 401) {
          try {
            const telegramUser = await authAPI.telegram(initData);
            if (active) setUser(telegramUser);
          } catch {
            // The regular login screen remains available if Telegram auth fails.
          }
        }
      } finally {
        if (active) setLoading(false);
      }
    }
    window.Telegram?.WebApp?.ready();
    window.Telegram?.WebApp?.expand();
    void restoreSession();
    return () => { active = false; };
  }, []);

  if (loading) return <LoadingScreen />;
  if (!user) return <AuthScreen onAuthenticated={setUser} />;
  if (runner) return <ExamRunner exam={runner.exam} initialAttempt={runner.attempt} onExit={() => setRunner(null)} />;
  return <Dashboard user={user} onOpenExam={async (examID) => {
    const [exam, attempt] = await Promise.all([examAPI.get(examID), examAPI.start(examID)]);
    setRunner({ exam, attempt });
  }} onLogout={async () => { await authAPI.logout(); setUser(null); }} />;
}

const SECTIONS = [
  { id: "overview", label: "Overview", short: "Home", icon: HomeIcon },
  { id: "exams", label: "Mock exams", short: "Exams", icon: BookIcon },
  { id: "progress", label: "Progress", short: "Progress", icon: ChartIcon }
] as const;

function useActiveSection() {
  const [active, setActive] = useState<string>(SECTIONS[0].id);
  useEffect(() => {
    const observer = new IntersectionObserver((entries) => {
      const visible = entries.filter((entry) => entry.isIntersecting).sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top);
      if (visible[0]) setActive(visible[0].target.id);
    }, { rootMargin: "-80px 0px -55% 0px" });
    SECTIONS.forEach(({ id }) => { const el = document.getElementById(id); if (el) observer.observe(el); });
    return () => observer.disconnect();
  }, []);
  return active;
}

function greeting() {
  const hour = new Date().getHours();
  return hour < 12 ? "Good morning" : hour < 18 ? "Good afternoon" : "Good evening";
}

function formatBand(value: number | null | undefined) {
  return value === null || value === undefined ? "—" : value.toFixed(1);
}

function Dashboard({ user, onLogout, onOpenExam }: { user: User; onLogout: () => Promise<void>; onOpenExam: (examID: string) => Promise<void> }) {
  const [exams, setExams] = useState<ExamSummary[] | null>(null);
  const [history, setHistory] = useState<HistoryItem[]>([]);
  const [progress, setProgress] = useState<SkillProgress[]>([]);
  const [dataError, setDataError] = useState("");
  const [opening, setOpening] = useState<string | null>(null);
  const [filter, setFilter] = useState<"all" | "listening" | "reading">("all");
  const active = useActiveSection();

  useEffect(() => {
    let alive = true;
    Promise.all([examAPI.list(), examAPI.history(), examAPI.progress()]).then(([examList, attempts, skills]) => {
      if (alive) { setExams(examList); setHistory(attempts); setProgress(skills); }
    }).catch((error) => {
      if (alive) { setExams([]); setDataError(error instanceof Error ? error.message : "Your dashboard could not be loaded."); }
    });
    return () => { alive = false; };
  }, []);

  const completed = progress.reduce((sum, item) => sum + item.completed_attempts, 0);
  const average = completed ? progress.reduce((sum, item) => sum + item.average_band * item.completed_attempts, 0) / completed : null;
  const best = progress.length ? Math.max(...progress.map((item) => item.best_band)) : null;
  const correct = progress.reduce((sum, item) => sum + item.correct_answers, 0);
  const total = progress.reduce((sum, item) => sum + item.total_questions, 0);
  const accuracy = total ? Math.round((correct / total) * 100) : null;
  const firstName = user.display_name.trim().split(/\s+/)[0] || "there";
  const visibleExams = (exams ?? []).filter((exam) => filter === "all" || exam.skill === filter);
  const skills = Array.from(new Set(["listening", "reading", ...(exams ?? []).map((exam) => exam.skill)]));

  async function openExam(id: string) {
    setOpening(id); setDataError("");
    try { await onOpenExam(id); }
    catch (error) { setDataError(error instanceof Error ? error.message : "This exam could not be opened."); setOpening(null); }
  }

  return (
    <div className="app">
      <header className="topbar">
        <div className="topbar-inner">
          <a href="#overview" aria-label="mock.ai home"><Brand /></a>
          <nav className="topnav" aria-label="Main">
            {SECTIONS.map(({ id, label, icon: Icon }) => (
              <a key={id} href={`#${id}`} className={active === id ? "active" : ""} aria-current={active === id ? "page" : undefined}><Icon />{label}</a>
            ))}
          </nav>
          <UserMenu user={user} onLogout={onLogout} />
        </div>
      </header>

      <main className="page">
        <section id="overview" className="page-header">
          <div>
            <h1>{greeting()}, {firstName}</h1>
            <p>{completed ? "Here's how your practice is going." : "Take your first mock test to get a band score."}</p>
          </div>
          <a className="btn btn-primary" href="#exams">Start a mock test <ArrowRightIcon /></a>
        </section>

        <div className="stats">
          <Stat highlight icon={<TargetIcon />} label="Average band" value={formatBand(average)} hint={completed ? `Across ${completed} test${completed === 1 ? "" : "s"}` : "No tests yet"} />
          <Stat icon={<TrophyIcon />} label="Best band" value={formatBand(best)} hint="Highest single result" />
          <Stat icon={<ListIcon />} label="Tests completed" value={String(completed)} hint="Listening and Reading" muted={!completed} />
          <Stat icon={<ChartIcon />} label="Accuracy" value={accuracy === null ? "—" : `${accuracy}%`} hint={total ? `${correct} of ${total} correct` : "Correct answers overall"} />
        </div>

        <section id="exams" className="section">
          <div className="section-head">
            <div><h2>Mock exams</h2><p>Each test follows the official timing.</p></div>
            <div className="filter-tabs" role="group" aria-label="Filter by skill">
              {(["all", "listening", "reading"] as const).map((key) => (
                <button key={key} aria-pressed={filter === key} onClick={() => setFilter(key)}>{key}</button>
              ))}
            </div>
          </div>
          {dataError && <div style={{ marginBottom: 16 }}><ErrorAlert>{dataError}</ErrorAlert></div>}
          {exams === null ? (
            <div className="exam-grid">{[0, 1, 2].map((i) => <div key={i} className="skeleton" style={{ height: 210 }} />)}</div>
          ) : visibleExams.length ? (
            <div className="exam-grid">
              {visibleExams.map((exam) => (
                <article className="card exam-card" key={exam.id}>
                  <div className="exam-card-top">
                    <span className={`chip chip-${exam.skill}`}><SkillIcon skill={exam.skill} />{exam.skill}</span>
                  </div>
                  <h3>{exam.title}</h3>
                  {exam.description && <p className="desc">{exam.description}</p>}
                  <div className="exam-meta">
                    <span><ListIcon />{exam.question_count} questions</span>
                    <span><ClockIcon />{Math.round(exam.duration_seconds / 60)} min</span>
                  </div>
                  <button className="btn btn-primary btn-block" disabled={opening !== null} onClick={() => void openExam(exam.id)}>
                    {opening === exam.id ? <><span className="spinner" aria-hidden="true" />Opening…</> : <>Start test <ArrowRightIcon /></>}
                  </button>
                </article>
              ))}
            </div>
          ) : (
            <div className="card empty">
              <div className="empty-icon"><InboxIcon /></div>
              <strong>{filter === "all" ? "No exams published yet" : `No ${filter} exams yet`}</strong>
              <p>New Listening and Reading tests will appear here as soon as they are published.</p>
            </div>
          )}
        </section>

        <section id="progress" className="section">
          <div className="section-head"><div><h2>Progress</h2><p>Your results by skill and your latest attempts.</p></div></div>
          <div className="split">
            <div className="card panel">
              <div className="panel-head"><h3>By skill</h3></div>
              {skills.map((skill) => {
                const item = progress.find((p) => p.skill === skill);
                const pct = item?.total_questions ? Math.round((item.correct_answers / item.total_questions) * 100) : 0;
                return (
                  <div className="skill-row" key={skill}>
                    <div className={`skill-icon ${skill}`}><SkillIcon skill={skill} /></div>
                    <div>
                      <div className="name">{skill}</div>
                      <div className="sub">{item ? `${item.completed_attempts} test${item.completed_attempts === 1 ? "" : "s"} · ${pct}% correct` : "Not attempted yet"}</div>
                      <div className={`bar ${skill}`} aria-hidden="true"><span style={{ width: `${pct}%` }} /></div>
                    </div>
                    <div className="band"><strong>{formatBand(item?.latest_band)}</strong><span>{item ? `Best ${formatBand(item.best_band)}` : "Latest band"}</span></div>
                  </div>
                );
              })}
            </div>

            <div className="card panel">
              <div className="panel-head"><h3>Recent attempts</h3></div>
              {history.length ? (
                <ul className="history-list">
                  {history.map((item) => (
                    <li key={item.id}>
                      <div>
                        <div className="title">{item.title}</div>
                        <div className="meta"><span>{item.skill}</span>·<span>{formatDate(item.submitted_at ?? item.started_at)}</span></div>
                      </div>
                      {item.status === "scored" ? (
                        <div className="score"><strong>Band {formatBand(item.band_score)}</strong><span>{item.raw_score ?? 0} / {item.question_count}</span></div>
                      ) : (
                        <span className="chip chip-warning">{item.status === "in_progress" ? "In progress" : item.status.replace(/_/g, " ")}</span>
                      )}
                    </li>
                  ))}
                </ul>
              ) : (
                <div className="empty">
                  <div className="empty-icon"><ClockIcon /></div>
                  <strong>No attempts yet</strong>
                  <p>Finish a test and your result will show up here.</p>
                </div>
              )}
            </div>
          </div>
        </section>
      </main>

      <nav className="bottom-nav" aria-label="Main">
        {SECTIONS.map(({ id, short, icon: Icon }) => (
          <a key={id} href={`#${id}`} className={active === id ? "active" : ""} aria-current={active === id ? "page" : undefined}><Icon /><span>{short}</span></a>
        ))}
      </nav>
    </div>
  );
}

function Stat({ icon, label, value, hint, highlight, muted }: { icon: ReactNode; label: string; value: string; hint: string; highlight?: boolean; muted?: boolean }) {
  return (
    <div className={`card stat${highlight ? " highlight" : ""}`}>
      <div className="stat-label">{icon}{label}</div>
      <div className={`stat-value${value === "—" || muted ? " muted" : ""}`}>{value}</div>
      <div className="stat-hint">{hint}</div>
    </div>
  );
}

function UserMenu({ user, onLogout }: { user: User; onLogout: () => Promise<void> }) {
  const [open, setOpen] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const closeConfirm = useCallback(() => setConfirming(false), []);

  useEffect(() => {
    if (!open) return;
    const onClick = (event: MouseEvent) => { if (!ref.current?.contains(event.target as Node)) setOpen(false); };
    const onKey = (event: KeyboardEvent) => { if (event.key === "Escape") setOpen(false); };
    document.addEventListener("mousedown", onClick);
    document.addEventListener("keydown", onKey);
    return () => { document.removeEventListener("mousedown", onClick); document.removeEventListener("keydown", onKey); };
  }, [open]);

  const avatar = user.avatar_url ? <img className="avatar" src={user.avatar_url} alt="" /> : <span className="avatar">{initialsOf(user.display_name)}</span>;
  return (
    <div className="user-menu" ref={ref}>
      <button className="user-trigger" aria-haspopup="menu" aria-expanded={open} aria-label="Account menu" onClick={() => setOpen((v) => !v)}>
        {avatar}<span className="name">{user.display_name}</span><ChevronDownIcon />
      </button>
      {open && (
        <div className="menu" role="menu">
          <div className="menu-header">
            <strong>{user.display_name}</strong>
            {user.email && <span>{user.email}</span>}
            <span className="chip chip-neutral">{user.roles.includes("admin") ? "Admin" : "Student"}</span>
          </div>
          <button role="menuitem" onClick={() => { setOpen(false); setConfirming(true); }}><LogoutIcon />Sign out</button>
        </div>
      )}
      {confirming && (
        <ConfirmDialog title="Sign out?" confirmLabel="Sign out" onCancel={closeConfirm} onConfirm={() => { setConfirming(false); void onLogout(); }}>
          Your progress is saved to your account. You can sign back in any time.
        </ConfirmDialog>
      )}
    </div>
  );
}

function formatDate(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat("en", { day: "numeric", month: "short" }).format(date);
}

function LoadingScreen() {
  return <div className="loading-screen" role="status"><span className="brand-mark"><LogoIcon /></span><span>Loading your practice space…</span></div>;
}

export default App;
