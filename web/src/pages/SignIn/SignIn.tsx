/** Token-mode sign-in page: an email and password form, for an instance served by `bdb serve`. */

import { type SubmitEvent, useState } from "react";
import { Navigate, useSearchParams } from "react-router-dom";
import { ApiError } from "../../api/client";
import { useAuth } from "../../auth";
import { BaguetteLogo } from "../../components/BaguetteLogo";
import { Button } from "../../components/Button";
import { Input } from "../../components/Input";
import { Skeleton } from "../../components/Skeleton";
import { isTokenMode } from "../../config";
import { isStandalone } from "../../standalone/isStandalone";
import styles from "./SignIn.module.css";

/**
 * Returns where a signed-in user goes from the sign-in page: the invite they opened before signing in, when the invite
 * page left its token for after the sign-in, or their bdbs. The invite page clears the token once it shows.
 */
function afterSignIn(): string {
  const inviteToken = sessionStorage.getItem("invite_token");
  if (inviteToken) return `/invite/${inviteToken}`;
  return isStandalone() ? "/app/bdbs" : "/bdbs";
}

/** Returns the message shown for a failed sign-in, in plain words when the server refused the email and password. */
function signInError(err: unknown): string {
  if (err instanceof ApiError && err.status === 401) return "Wrong email or password.";
  return err instanceof Error ? err.message : "Signing in failed";
}

export function SignIn() {
  const { isAuthenticated, isLoading, signInWithPassword } = useAuth();
  const [searchParams] = useSearchParams();
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  async function handleSubmit(e: SubmitEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = new FormData(e.currentTarget);
    const email = (form.get("email") as string).trim();
    const password = form.get("password") as string;
    if (!email || !password) return;
    setError(null);
    setSubmitting(true);
    // Once signed in, the page re-renders authenticated and redirects where afterSignIn says.
    try {
      await signInWithPassword(email, password);
    } catch (err) {
      setError(signInError(err));
    } finally {
      setSubmitting(false);
    }
  }

  if (!isTokenMode()) {
    return <Navigate to="/" replace />;
  }

  if (isLoading) {
    return (
      <div className={styles.container}>
        <Skeleton className={styles.loadingCard} />
      </div>
    );
  }

  if (isAuthenticated) {
    return <Navigate to={afterSignIn()} replace />;
  }

  return (
    <div className={styles.container}>
      <div className={styles.card}>
        <div className={styles.logo}>
          <BaguetteLogo size={48} />
        </div>
        <h1 className={styles.title}>Sign in to BaguetteDB</h1>

        {searchParams.has("ended") && <p className={styles.notice}>Your session has ended.</p>}

        <form onSubmit={handleSubmit} className={styles.form}>
          <Input label="Email" name="email" type="email" autoComplete="username" required />
          <Input label="Password" name="password" type="password" autoComplete="current-password" required />
          {error && (
            <p className={styles.error} role="alert">
              {error}
            </p>
          )}
          <Button type="submit" loading={submitting}>
            Sign in
          </Button>
        </form>

        <p className={styles.text}>
          Forgot your password? On the machine serving this BaguetteDB, <code>bdb init</code> sets yours if you run
          it, and <code>bdb users set-password &lt;email&gt;</code> anyone&apos;s.
        </p>
      </div>
    </div>
  );
}
