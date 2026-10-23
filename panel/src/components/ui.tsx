import { useEffect, useId, useRef, useState, type InputHTMLAttributes, type ReactNode } from "react";
import { ApiError } from "../api/client";
import { GLOSSARY, type GlossaryKey } from "../lib/glossary";

/** An (i) icon that explains a term on hover, focus or tap. */
export function InfoTip({ term, label }: { term: GlossaryKey; label?: string }) {
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null);
  const btn = useRef<HTMLButtonElement>(null);
  const id = useId();
  const show = () => {
    const r = btn.current?.getBoundingClientRect();
    if (!r) return;
    const width = Math.min(300, window.innerWidth - 24);
    setPos({ left: Math.max(12, Math.min(r.left + r.width / 2 - width / 2, window.innerWidth - width - 12)), top: r.bottom + 6 });
  };
  useEffect(() => {
    if (!pos) return;
    const hide = () => setPos(null);
    window.addEventListener("scroll", hide, true);
    return () => window.removeEventListener("scroll", hide, true);
  }, [pos]);
  return (
    <span className="info" onMouseEnter={show} onMouseLeave={() => setPos(null)}>
      <button
        ref={btn}
        type="button"
        className="info-btn"
        aria-label={`What is ${label ?? "this"}?`}
        aria-describedby={pos ? id : undefined}
        onFocus={show}
        onBlur={() => setPos(null)}
        onClick={(e) => {
          e.preventDefault();
          if (pos) setPos(null);
          else show();
        }}
      >
        i
      </button>
      {pos && (
        <span role="tooltip" id={id} className="info-pop" style={{ left: pos.left, top: pos.top }}>
          {GLOSSARY[term]}
        </span>
      )}
    </span>
  );
}

/** A label with an optional (i) explanation. */
export function Term({ children, info }: { children: ReactNode; info?: GlossaryKey }) {
  return (
    <span className="term">
      {children}
      {info && <InfoTip term={info} label={typeof children === "string" ? children : undefined} />}
    </span>
  );
}

type Tone = "good" | "warning" | "critical" | "neutral";

export function Badge({ tone = "neutral", plain, children }: { tone?: Tone; plain?: boolean; children: ReactNode }) {
  return <span className={`badge ${tone === "neutral" ? "" : tone} ${plain ? "plain" : ""}`}>{children}</span>;
}

export function Card({ title, actions, children }: { title?: ReactNode; actions?: ReactNode; children: ReactNode }) {
  return (
    <section className="card">
      {(title || actions) && (
        <div className="card-head">
          {title ? <h2>{title}</h2> : <span />}
          {actions && <div className="actions">{actions}</div>}
        </div>
      )}
      {children}
    </section>
  );
}

export function PageHead({ title, sub, actions }: { title: ReactNode; sub?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="page-head">
      <div>
        <h1>{title}</h1>
        {sub && <p>{sub}</p>}
      </div>
      {actions && <div className="actions">{actions}</div>}
    </div>
  );
}

export function Field({
  label,
  info,
  hint,
  error,
  className,
  children,
}: {
  label: ReactNode;
  info?: GlossaryKey;
  hint?: ReactNode;
  error?: ReactNode;
  className?: string;
  children: (id: string) => ReactNode;
}) {
  const id = useId();
  return (
    <div className={`field ${className ?? ""}`}>
      <span className="term">
        <label htmlFor={id}>{label}</label>
        {info && <InfoTip term={info} label={typeof label === "string" ? label : undefined} />}
      </span>
      {children(id)}
      {error ? <div className="field-error">{error}</div> : hint ? <div className="field-hint">{hint}</div> : null}
    </div>
  );
}

export function Check({
  checked,
  onChange,
  children,
  disabled,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  children: ReactNode;
  disabled?: boolean;
}) {
  return (
    <label className="check">
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      {children}
    </label>
  );
}

/** The number in a whole-number field: null when empty, NaN when not a whole number. */
export function parseWhole(text: string): number | null {
  const t = text.trim();
  if (t === "") return null;
  return /^\d+$/.test(t) ? Number(t) : NaN;
}

/** A whole-number field. What is typed is never rounded or clamped behind the
 *  user's back: an invalid entry reaches onChange as NaN for the form to flag.
 *  Leading zeros are dropped when the field loses focus. */
export function WholeInput({
  value,
  onChange,
  className = "input",
  ...rest
}: { value: number | null; onChange: (v: number | null) => void } & Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange" | "type">) {
  const shown = (v: number | null) => (v === null || Number.isNaN(v) ? "" : String(v));
  const [text, setText] = useState(shown(value));
  // Follow changes made elsewhere; keep the typed text while it means the same.
  useEffect(() => setText((t) => (Object.is(parseWhole(t), value) ? t : shown(value))), [value]);
  const v = parseWhole(text);
  return (
    <input
      {...rest}
      className={Number.isNaN(v) ? `${className} invalid` : className}
      inputMode="numeric"
      value={text}
      aria-invalid={Number.isNaN(v) || undefined}
      onChange={(e) => {
        setText(e.target.value);
        onChange(parseWhole(e.target.value));
      }}
      onBlur={() => !Number.isNaN(v) && setText(shown(v))}
    />
  );
}

export function Segmented<T extends string>({
  value,
  options,
  onChange,
  label,
}: {
  value: T;
  options: { value: T; label: string }[];
  onChange: (v: T) => void;
  label: string;
}) {
  return (
    <div className="segmented" role="radiogroup" aria-label={label}>
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={value === o.value}
          className={value === o.value ? "on" : ""}
          onClick={() => onChange(o.value)}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

export function Tabs<T extends string>({
  tabs,
  value,
  onChange,
}: {
  tabs: { value: T; label: ReactNode; disabled?: boolean; title?: string }[];
  value: T;
  onChange: (v: T) => void;
}) {
  return (
    <div className="tabs" role="tablist">
      {tabs.map((t) => (
        <button
          key={t.value}
          type="button"
          role="tab"
          aria-selected={value === t.value}
          className={`tab ${value === t.value ? "on" : ""}`}
          disabled={t.disabled}
          title={t.title}
          onClick={() => onChange(t.value)}
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}

export function Spinner() {
  return <div className="spinner" role="status" aria-label="Loading" />;
}

export function Loading() {
  return (
    <div className="center">
      <Spinner />
    </div>
  );
}

export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null;
  const msg = error instanceof Error ? error.message : String(error);
  return <div className="alert critical">{msg}</div>;
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <h2>{title}</h2>
      {children}
    </div>
  );
}

export function Modal({
  title,
  onClose,
  children,
  actions,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  actions?: ReactNode;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div className="modal-back" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal" role="dialog" aria-modal="true" aria-label={title}>
        <h2>{title}</h2>
        {children}
        {actions && <div className="modal-actions">{actions}</div>}
      </div>
    </div>
  );
}

/** A button that asks for confirmation in a modal before running an action. */
export function ConfirmButton({
  label,
  title,
  message,
  confirmLabel = "Confirm",
  danger,
  small,
  onConfirm,
  disabled,
}: {
  label: ReactNode;
  title: string;
  message: ReactNode;
  confirmLabel?: string;
  danger?: boolean;
  small?: boolean;
  onConfirm: () => Promise<unknown> | void;
  disabled?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const run = async () => {
    setBusy(true);
    setError(null);
    try {
      await onConfirm();
      setOpen(false);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <button
        type="button"
        className={`btn ${danger ? "danger" : ""} ${small ? "sm" : ""}`}
        disabled={disabled}
        onClick={() => setOpen(true)}
      >
        {label}
      </button>
      {open && (
        <Modal
          title={title}
          onClose={() => setOpen(false)}
          actions={
            <>
              <button type="button" className="btn" onClick={() => setOpen(false)}>
                Cancel
              </button>
              <button type="button" className={`btn ${danger ? "danger" : "primary"}`} disabled={busy} onClick={run}>
                {busy ? "Working…" : confirmLabel}
              </button>
            </>
          }
        >
          <div className="stack" style={{ gap: 12 }}>
            <div>{message}</div>
            <ErrorBox error={error} />
          </div>
        </Modal>
      )}
    </>
  );
}

export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof Error) return e.message;
  return String(e);
}
