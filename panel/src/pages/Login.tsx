import { useState, type FormEvent } from "react";
import { Navigate, useLocation, useNavigate } from "react-router-dom";
import { ApiError } from "../api/client";
import { useLogin, useMe } from "../api/queries";
import { ErrorBox, Field } from "../components/ui";
import { Brand } from "../layout/AppShell";

export default function Login() {
  const me = useMe();
  const login = useLogin();
  const navigate = useNavigate();
  const location = useLocation();
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [needTotp, setNeedTotp] = useState(false);
  const [error, setError] = useState<unknown>(null);

  if (me.data) return <Navigate to="/" replace />;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    login.mutate(
      { username, password, totp_code: needTotp ? code : undefined },
      {
        onSuccess: () => navigate((location.state as { from?: string })?.from ?? "/", { replace: true }),
        onError: (err) => {
          if (err instanceof ApiError && (err as ApiError & { totpRequired?: boolean }).totpRequired) {
            if (needTotp) setError(err);
            setNeedTotp(true);
            setCode("");
          } else {
            setError(err);
          }
        },
      },
    );
  };

  return (
    <div className="login-wrap">
      <form className="card login stack" onSubmit={submit}>
        <Brand />
        {!needTotp ? (
          <>
            <Field label="Username">
              {(id) => (
                <input id={id} className="input" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required />
              )}
            </Field>
            <Field label="Password">
              {(id) => (
                <input
                  id={id}
                  className="input"
                  type="password"
                  autoComplete="current-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  autoFocus
                  required
                />
              )}
            </Field>
          </>
        ) : (
          <Field label="Authenticator code" hint="Enter the 6-digit code from your authenticator app.">
            {(id) => (
              <input
                id={id}
                className="input mono"
                inputMode="numeric"
                autoComplete="one-time-code"
                pattern="\d{6}"
                maxLength={6}
                value={code}
                onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
                autoFocus
                required
              />
            )}
          </Field>
        )}
        <ErrorBox error={error} />
        <button className="btn primary" disabled={login.isPending}>
          {login.isPending ? "Signing in…" : needTotp ? "Verify" : "Sign in"}
        </button>
        {needTotp && (
          <button type="button" className="btn ghost" onClick={() => setNeedTotp(false)}>
            Back
          </button>
        )}
      </form>
    </div>
  );
}
