import { ReactNode, useEffect, useRef } from "react";

type IconProps = { className?: string };
const svg = (children: ReactNode) => ({ className }: IconProps) => (
  <svg viewBox="0 0 24 24" aria-hidden="true" className={className}>{children}</svg>
);

export const LogoIcon = svg(<><path d="M4 19V6l8 7 8-7v13" /></>);
export const HomeIcon = svg(<><path d="M3 10.5 12 3l9 7.5" /><path d="M5 9.5V20h14V9.5" /><path d="M10 20v-6h4v6" /></>);
export const BookIcon = svg(<><path d="M4 5.5A2.5 2.5 0 0 1 6.5 3H11v16H6.5A2.5 2.5 0 0 0 4 21.5v-16Z" /><path d="M20 5.5A2.5 2.5 0 0 0 17.5 3H13v16h4.5a2.5 2.5 0 0 1 2.5 2.5v-16Z" /></>);
export const HeadphonesIcon = svg(<><path d="M4 14v-2a8 8 0 0 1 16 0v2" /><path d="M4 14h3v7H6a2 2 0 0 1-2-2v-5Zm16 0h-3v7h1a2 2 0 0 0 2-2v-5Z" /></>);
export const ChartIcon = svg(<><path d="M4 20V10m6 10V4m6 16v-7m4 7H2" /></>);
export const ClockIcon = svg(<><circle cx="12" cy="12" r="9" /><path d="M12 7v5l3 2" /></>);
export const ListIcon = svg(<><path d="M9 6h11M9 12h11M9 18h11" /><path d="M4.5 6h.01M4.5 12h.01M4.5 18h.01" /></>);
export const TrophyIcon = svg(<><path d="M8 4h8v5a4 4 0 0 1-8 0V4Z" /><path d="M16 5h3v2a3 3 0 0 1-3 3M8 5H5v2a3 3 0 0 0 3 3M12 13v4m-4 4h8m-6-4h4" /></>);
export const TargetIcon = svg(<><circle cx="12" cy="12" r="9" /><circle cx="12" cy="12" r="5" /><circle cx="12" cy="12" r="1" /></>);
export const CheckIcon = svg(<><path d="m5 12.5 4.5 4.5L19 7.5" /></>);
export const CheckCircleIcon = svg(<><circle cx="12" cy="12" r="9" /><path d="m8 12.5 2.8 2.8L16.5 9.5" /></>);
export const AlertIcon = svg(<><circle cx="12" cy="12" r="9" /><path d="M12 7.5v5.5M12 16.5h.01" /></>);
export const ArrowRightIcon = svg(<><path d="M5 12h14m-6-6 6 6-6 6" /></>);
export const ArrowLeftIcon = svg(<><path d="M19 12H5m6-6-6 6 6 6" /></>);
export const ChevronDownIcon = svg(<><path d="m6 9 6 6 6-6" /></>);
export const LogoutIcon = svg(<><path d="M15 4h3a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-3" /><path d="M10 16l-4-4 4-4M6 12h10" /></>);
export const EyeIcon = svg(<><path d="M2.5 12S6 5 12 5s9.5 7 9.5 7-3.5 7-9.5 7-9.5-7-9.5-7Z" /><circle cx="12" cy="12" r="3" /></>);
export const EyeOffIcon = svg(<><path d="M3 3l18 18" /><path d="M10.6 5.1A9.7 9.7 0 0 1 12 5c6 0 9.5 7 9.5 7a17 17 0 0 1-2.9 3.8M6.6 6.6A16.6 16.6 0 0 0 2.5 12S6 19 12 19a9.3 9.3 0 0 0 4.4-1.1" /><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2" /></>);
export const InboxIcon = svg(<><path d="M3 13h5l1.5 3h5L16 13h5" /><path d="M5.5 5h13L21 13v6H3v-6l2.5-8Z" /></>);

export function GoogleIcon() {
  return <svg viewBox="0 0 24 24" aria-hidden="true" className="google-icon"><path d="M21.6 12.2c0-.7-.1-1.5-.2-2.2H12v4h5.4a4.6 4.6 0 0 1-2 3v2.6h3.3c1.9-1.8 2.9-4.4 2.9-7.4Z"/><path d="M12 22c2.7 0 5-.9 6.7-2.4L15.4 17c-.9.6-2.1 1-3.4 1a5.9 5.9 0 0 1-5.6-4.1H3v2.7A10 10 0 0 0 12 22Z"/><path d="M6.4 13.9A6 6 0 0 1 6.1 12c0-.7.1-1.3.3-1.9V7.4H3A10 10 0 0 0 2 12c0 1.6.4 3.2 1 4.6l3.4-2.7Z"/><path d="M12 6c1.5 0 2.8.5 3.9 1.5l2.9-2.9A9.8 9.8 0 0 0 12 2a10 10 0 0 0-9 5.4l3.4 2.7A5.9 5.9 0 0 1 12 6Z"/></svg>;
}

export function SkillIcon({ skill }: { skill: string }) {
  return skill === "listening" ? <HeadphonesIcon /> : <BookIcon />;
}

export function Brand() {
  return <span className="brand"><span className="brand-mark"><LogoIcon /></span>mock.ai</span>;
}

export function ErrorAlert({ children }: { children: ReactNode }) {
  return <div className="alert alert-error" role="alert"><AlertIcon /><span>{children}</span></div>;
}

type ConfirmProps = {
  title: string;
  children: ReactNode;
  confirmLabel: string;
  cancelLabel?: string;
  tone?: "primary" | "danger";
  onConfirm: () => void;
  onCancel: () => void;
};

export function ConfirmDialog({ title, children, confirmLabel, cancelLabel = "Cancel", tone = "primary", onConfirm, onCancel }: ConfirmProps) {
  const cancelRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    cancelRef.current?.focus();
    const onKey = (event: KeyboardEvent) => { if (event.key === "Escape") onCancel(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onCancel]);
  return (
    <div className="modal-backdrop" onClick={onCancel}>
      <div className="modal" role="alertdialog" aria-modal="true" aria-labelledby="confirm-title" onClick={(event) => event.stopPropagation()}>
        <h2 id="confirm-title">{title}</h2>
        <p>{children}</p>
        <div className="modal-actions">
          <button ref={cancelRef} className="btn btn-secondary" onClick={onCancel}>{cancelLabel}</button>
          <button className={`btn ${tone === "danger" ? "btn-danger" : "btn-primary"}`} onClick={onConfirm}>{confirmLabel}</button>
        </div>
      </div>
    </div>
  );
}

export function initialsOf(name: string) {
  return name.trim().split(/\s+/).slice(0, 2).map((part) => part[0] ?? "").join("").toUpperCase() || "?";
}
