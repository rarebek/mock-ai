import { FormEvent, useState } from "react";
import { authAPI, User } from "./auth";
import { Brand, CheckIcon, ErrorAlert, EyeIcon, EyeOffIcon, GoogleIcon } from "./ui";

type Props = { onAuthenticated: (user: User) => void };

export default function AuthScreen({ onAuthenticated }: Props) {
  const [mode, setMode] = useState<"login" | "register">("login");
  const [displayName, setDisplayName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const registering = mode === "register";

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    setSubmitting(true);
    try {
      const user = registering
        ? await authAPI.register(displayName, email, password)
        : await authAPI.login(email, password);
      onAuthenticated(user);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "Could not authenticate.");
    } finally {
      setSubmitting(false);
    }
  }

  function switchMode() {
    setMode((current) => current === "login" ? "register" : "login");
    setError("");
  }

  return (
    <main className="auth-page">
      <section className="auth-intro">
        <Brand />
        <div className="auth-intro-body">
          <h1>IELTS mock tests that feel like the real exam.</h1>
          <p>Practise Listening and Reading under real timing, and see your band score the moment you finish.</p>
          <ul className="feature-list">
            <li><CheckIcon /> Full-length tests with the official time limits</li>
            <li><CheckIcon /> Instant band score with every answer marked</li>
            <li><CheckIcon /> Track your progress in each skill over time</li>
          </ul>
        </div>
        <footer>Answers save automatically as you work.</footer>
      </section>

      <section className="auth-panel">
        <div className="auth-card">
          <Brand />
          <h2>{registering ? "Create your account" : "Welcome back"}</h2>
          <p className="auth-subtitle">
            {registering ? "Start practising in less than a minute." : "Sign in to continue your practice."}
          </p>

          <a className="btn btn-secondary btn-lg btn-block" href="/api/v1/auth/google/start">
            <GoogleIcon /> Continue with Google
          </a>
          <div className="auth-divider"><span>or use email</span></div>

          <form onSubmit={submit}>
            {registering && (
              <label className="field">
                <span>Full name</span>
                <input className="input" autoComplete="name" value={displayName} onChange={(event) => setDisplayName(event.target.value)} required minLength={2} maxLength={100} placeholder="Your name" />
              </label>
            )}
            <label className="field">
              <span>Email</span>
              <input className="input" type="email" autoComplete="email" value={email} onChange={(event) => setEmail(event.target.value)} required placeholder="you@example.com" />
            </label>
            <div className="field">
              <label htmlFor="password" className="field-label">Password</label>
              <div className="input-wrap">
                <input id="password" className="input" type={showPassword ? "text" : "password"} autoComplete={registering ? "new-password" : "current-password"} value={password} onChange={(event) => setPassword(event.target.value)} required minLength={10} maxLength={128} aria-describedby={registering ? "password-hint" : undefined} />
                <button type="button" onClick={() => setShowPassword((v) => !v)} aria-label={showPassword ? "Hide password" : "Show password"}>
                  {showPassword ? <EyeOffIcon /> : <EyeIcon />}
                </button>
              </div>
              {registering && <small id="password-hint">At least 10 characters.</small>}
            </div>
            {error && <ErrorAlert>{error}</ErrorAlert>}
            <button className="btn btn-primary btn-lg btn-block" disabled={submitting}>
              {submitting && <span className="spinner" aria-hidden="true" />}
              {submitting ? "Please wait…" : registering ? "Create account" : "Sign in"}
            </button>
          </form>

          <p className="auth-switch">
            {registering ? "Already have an account? " : "New to mock.ai? "}
            <button type="button" onClick={switchMode}>{registering ? "Sign in" : "Create an account"}</button>
          </p>
        </div>
      </section>
    </main>
  );
}
