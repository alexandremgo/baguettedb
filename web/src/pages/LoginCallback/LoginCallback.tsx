import { useEffect, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { useAuth } from "../../auth";
import { isStandalone } from "../../standalone/isStandalone";
import styles from "./LoginCallback.module.css";

export function LoginCallback() {
  const [searchParams] = useSearchParams();
  const { handleCallback } = useAuth();
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  const exchanged = useRef(false);

  const code = searchParams.get("code");
  const state = searchParams.get("state");
  // Reads the saved state once on mount (lazy initializer). A successful exchange deletes pkce_state from
  // sessionStorage before this component unmounts, so re-reading it on later renders would spuriously fail
  // validation mid-exchange and flash a "Invalid state parameter" error on every login.
  const [savedState] = useState(() => sessionStorage.getItem("pkce_state"));

  const validationError = !code
    ? "Missing authorization code"
    : state !== savedState
      ? "Invalid state parameter"
      : null;

  useEffect(() => {
    if (validationError || !code || exchanged.current) return;
    exchanged.current = true;

    handleCallback(code)
      .then(() => {
        const inviteToken = sessionStorage.getItem("invite_token");
        if (inviteToken) {
          sessionStorage.removeItem("invite_token");
          navigate(`/invite/${inviteToken}`, { replace: true });
        } else {
          navigate(isStandalone() ? "/app/bdbs" : "/bdbs", { replace: true });
        }
      })
      .catch((err) => setError(err instanceof Error ? err.message : "Login failed"));
  }, [validationError, code, handleCallback, navigate]);

  const displayError = validationError ?? error;

  if (displayError) {
    return (
      <div className={styles.container}>
        <p className={styles.error}>Login failed: {displayError}</p>
        <a href="/" className={styles.link}>Back to home</a>
      </div>
    );
  }

  return (
    <div className={styles.container}>
      <p className={styles.loading}>Signing you in...</p>
    </div>
  );
}
