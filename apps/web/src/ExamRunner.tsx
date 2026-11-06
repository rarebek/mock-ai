import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Attempt, Exam, examAPI, Question } from "./exams";
import { ArrowLeftIcon, CheckIcon, ClockIcon, ConfirmDialog, ErrorAlert } from "./ui";

type Props = { exam: Exam; initialAttempt: Attempt; onExit: () => void };

export default function ExamRunner({ exam, initialAttempt, onExit }: Props) {
  const [attempt, setAttempt] = useState(initialAttempt);
  const [answers, setAnswers] = useState<Record<string, string[]>>(() => Object.fromEntries(initialAttempt.answers.map((a) => [a.question_id, a.values])));
  const [saving, setSaving] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [confirm, setConfirm] = useState<"exit" | "submit" | null>(null);
  const [now, setNow] = useState(Date.now());
  const expirySubmitted = useRef(false);
  const questions = useMemo(() => exam.sections.flatMap((section) => section.questions), [exam]);
  const closeConfirm = useCallback(() => setConfirm(null), []);

  useEffect(() => {
    if (attempt.status !== "in_progress") return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [attempt.status]);

  const remaining = Math.max(0, Math.floor((new Date(attempt.deadline_at).getTime() - now) / 1000));
  const clock = `${String(Math.floor(remaining / 60)).padStart(2, "0")}:${String(remaining % 60).padStart(2, "0")}`;
  const answeredCount = questions.filter((q) => isAnswered(answers[q.id])).length;
  const unanswered = questions.length - answeredCount;

  useEffect(() => {
    if (remaining === 0 && attempt.status === "in_progress" && !expirySubmitted.current) {
      expirySubmitted.current = true;
      setConfirm(null);
      void submit();
    }
  });

  async function save(questionID: string, values = answers[questionID] || []) {
    if (!values.length || attempt.status !== "in_progress") return;
    setSaving(questionID); setError("");
    try { await examAPI.save(attempt.id, questionID, values); }
    catch (caught) { setError(caught instanceof Error ? caught.message : "Your answer could not be saved."); }
    finally { setSaving(null); }
  }

  async function submit() {
    setSubmitting(true); setError("");
    try { setAttempt(await examAPI.submit(attempt.id)); window.scrollTo(0, 0); }
    catch (caught) { setError(caught instanceof Error ? caught.message : "Your test could not be submitted."); }
    finally { setSubmitting(false); }
  }

  if (attempt.status === "scored") return <Result exam={exam} attempt={attempt} questions={questions} onExit={onExit} />;

  return <main className="runner">
    <header className="runner-header">
      <div className="runner-header-inner">
        <div><button className="btn btn-ghost" onClick={() => setConfirm("exit")}><ArrowLeftIcon /><span className="label">Exit</span></button></div>
        <div className="runner-title"><strong>{exam.title}</strong><span>{exam.skill}</span></div>
        <time className={`timer${remaining < 300 ? " urgent" : ""}`} aria-label={`Time remaining ${clock}`}><ClockIcon />{clock}</time>
      </div>
      <div className="runner-progress" role="progressbar" aria-label="Questions answered" aria-valuemin={0} aria-valuemax={questions.length} aria-valuenow={answeredCount}>
        <span style={{ width: `${questions.length ? (answeredCount / questions.length) * 100 : 0}%` }} />
      </div>
    </header>

    <div className="runner-body">
      <aside className="navigator">
        <h2>Questions</h2>
        <p>{answeredCount} of {questions.length} answered</p>
        <nav className="question-map" aria-label="Jump to question">
          {questions.map((q, i) => <a key={q.id} className={isAnswered(answers[q.id]) ? "answered" : ""} href={`#question-${q.id}`} aria-label={`Question ${i + 1}${isAnswered(answers[q.id]) ? ", answered" : ""}`}>{i + 1}</a>)}
        </nav>
        <div className="legend"><span><i className="filled" />Answered</span><span><i />Not yet</span></div>
      </aside>

      <div className="exam-content">
        {exam.sections.map((section) => <section className="runner-section" key={section.id}>
          <div className="card material">
            <p className="eyebrow">Part {section.position}</p>
            <h2>{section.title}</h2>
            {section.instructions && <p className="instructions">{section.instructions}</p>}
            {section.audio_url && <audio controls src={section.audio_url} />}
            {section.passage && <article>{section.passage}</article>}
          </div>
          <div className="questions">
            {section.questions.map((question) => <QuestionInput
              key={question.id}
              question={question}
              value={answers[question.id] || []}
              saving={saving === question.id}
              onChange={(values, immediate) => { setAnswers((current) => ({ ...current, [question.id]: values })); if (immediate) void save(question.id, values); }}
              onBlur={() => void save(question.id)}
            />)}
          </div>
        </section>)}

        {error && <div style={{ marginBottom: 16 }}><ErrorAlert>{error}</ErrorAlert></div>}
        <div className="card submit-bar">
          <div>
            <strong>{unanswered ? `${unanswered} question${unanswered === 1 ? "" : "s"} left` : "All questions answered"}</strong>
            <span>Your test is marked as soon as you submit.</span>
          </div>
          <button className="btn btn-primary btn-lg" disabled={submitting} onClick={() => unanswered ? setConfirm("submit") : void submit()}>
            {submitting ? <><span className="spinner" aria-hidden="true" />Marking…</> : "Submit test"}
          </button>
        </div>
      </div>
    </div>

    {confirm === "exit" && (
      <ConfirmDialog title="Leave this test?" confirmLabel="Leave test" tone="danger" cancelLabel="Keep going" onCancel={closeConfirm} onConfirm={onExit}>
        Your answers so far are saved, but the timer keeps running while you are away.
      </ConfirmDialog>
    )}
    {confirm === "submit" && (
      <ConfirmDialog title="Submit with unanswered questions?" confirmLabel="Submit anyway" cancelLabel="Go back" onCancel={closeConfirm} onConfirm={() => { setConfirm(null); void submit(); }}>
        You have {unanswered} unanswered question{unanswered === 1 ? "" : "s"}. They will be marked as incorrect.
      </ConfirmDialog>
    )}
  </main>;
}

function isAnswered(values?: string[]) {
  return !!values?.some((v) => v.trim() !== "");
}

function QuestionInput({ question, value, saving, onChange, onBlur }: { question: Question; value: string[]; saving: boolean; onChange: (value: string[], immediate: boolean) => void; onBlur: () => void }) {
  const multiple = question.type === "multiple_select";
  const answered = isAnswered(value);
  return <article className={`card question${answered ? " is-answered" : ""}`} id={`question-${question.id}`}>
    <div className="question-number">{question.position}</div>
    <div>
      <h3 id={`prompt-${question.id}`}>{question.prompt}</h3>
      {question.options.length > 0 ? (
        <div className="options" role={multiple ? "group" : "radiogroup"} aria-labelledby={`prompt-${question.id}`}>
          {question.options.map((option) => {
            const selected = value.includes(option.value);
            return <label className={`option${selected ? " selected" : ""}`} key={option.id}>
              <input type={multiple ? "checkbox" : "radio"} name={question.id} checked={selected} onChange={() => {
                const next = multiple ? (selected ? value.filter((v) => v !== option.value) : [...value, option.value]) : [option.value];
                onChange(next, next.length > 0);
              }} />
              <span className="option-key">{option.label}</span>
              <span>{option.value}</span>
            </label>;
          })}
        </div>
      ) : (
        <input className="input" aria-labelledby={`prompt-${question.id}`} value={value[0] || ""} onChange={(event) => onChange([event.target.value], false)} onBlur={onBlur} placeholder="Type your answer" />
      )}
      <div className="save-state" aria-live="polite">
        {saving ? <><span className="spinner" aria-hidden="true" />Saving…</> : answered ? <><CheckIcon />Saved</> : null}
      </div>
    </div>
  </article>;
}

function Result({ exam, attempt, questions, onExit }: { exam: Exam; attempt: Attempt; questions: Question[]; onExit: () => void }) {
  const byQuestion = new Map(attempt.answers.map((a) => [a.question_id, a]));
  const correct = attempt.raw_score ?? 0;
  const total = attempt.question_count || questions.length;
  const accuracy = total ? Math.round((correct / total) * 100) : 0;
  const band = attempt.band_score ?? 0;
  const minutes = attempt.submitted_at ? Math.max(1, Math.round((new Date(attempt.submitted_at).getTime() - new Date(attempt.started_at).getTime()) / 60000)) : null;

  return <main className="result-page">
    <div className="card result-card">
      <span className="chip chip-success"><CheckIcon />Test complete</span>
      <h1>{exam.title}</h1>
      <p>{exam.skill}</p>

      <div className="band-ring" style={{ ["--pct" as string]: (band / 9) * 100 }}>
        <div><strong>{band.toFixed(1)}</strong><span>Band score</span></div>
      </div>

      <dl className="result-stats">
        <div><dt>Correct</dt><dd>{correct} / {total}</dd></div>
        <div><dt>Accuracy</dt><dd>{accuracy}%</dd></div>
        <div><dt>Time taken</dt><dd>{minutes === null ? "—" : `${minutes} min`}</dd></div>
      </dl>

      {attempt.answers.some((a) => a.is_correct !== undefined) && <div className="review">
        <h2>Question by question</h2>
        <div className="review-grid">
          {questions.map((q, i) => {
            const answer = byQuestion.get(q.id);
            const state = !answer || !isAnswered(answer.values) ? "skipped" : answer.is_correct ? "correct" : "wrong";
            return <span key={q.id} className={state} title={`Question ${i + 1}: ${state === "wrong" ? "incorrect" : state}`}>{i + 1}</span>;
          })}
        </div>
        <div className="legend"><span><i className="correct" />Correct</span><span><i className="wrong" />Incorrect</span><span><i />Not answered</span></div>
      </div>}

      <button className="btn btn-primary btn-lg btn-block" onClick={onExit}>Back to dashboard</button>
    </div>
  </main>;
}
