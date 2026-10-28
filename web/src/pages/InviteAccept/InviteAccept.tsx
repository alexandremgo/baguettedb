/** Invite accept page — shown when a user opens an invite link. */

import { useEffect } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useAuth } from "../../auth";
import { BaguetteLogo } from "../../components/BaguetteLogo";
import { Button } from "../../components/Button";
import { Skeleton } from "../../components/Skeleton";
import { useToast } from "../../components/Toast";
import { useInviteDetails } from "../../hooks/useInvites";
import { useAcceptInvite } from "../../hooks/useInviteMutations";
import { ApiError } from "../../api/client";
import styles from "./InviteAccept.module.css";

export function InviteAccept() {
  const { token } = useParams<{ token: string }>();
  const { isAuthenticated, isLoading: authLoading, login } = useAuth();
  const { toast } = useToast();
  const navigate = useNavigate();

  const { data: invite, isLoading: inviteLoading, error: inviteError } = useInviteDetails(
    isAuthenticated ? token : undefined,
  );
  const acceptMutation = useAcceptInvite();

  // The invite left for after a sign-in is reached: the sign-in page stops sending the user here.
  useEffect(() => {
    if (isAuthenticated) sessionStorage.removeItem("invite_token");
  }, [isAuthenticated]);

  function handleAccept() {
    if (!token) return;
    acceptMutation.mutate(token, {
      onSuccess: (bdb) => {
        toast(`Joined "${bdb.name}"!`, "success");
        navigate(`/bdbs/${bdb.id}`, { replace: true });
      },
      onError: (err) => {
        toast(err instanceof Error ? err.message : "Failed to join bdb", "error");
      },
    });
  }

  function handleLogin() {
    if (token) {
      sessionStorage.setItem("invite_token", token);
    }
    login();
  }

  if (authLoading) {
    return (
      <div className={styles.container}>
        <Skeleton className={styles.loadingCard} />
      </div>
    );
  }

  // Not authenticated — prompt to sign in.
  if (!isAuthenticated) {
    return (
      <div className={styles.container}>
        <div className={styles.card}>
          <span className={styles.logo}>
            <BaguetteLogo size={30} />
          </span>
          <p className={styles.title}>You've been invited to a bdb</p>
          <p className={styles.subtitle}>Sign in to accept the invitation and join.</p>
          <Button onClick={handleLogin}>Sign in to join</Button>
        </div>
      </div>
    );
  }

  // Loading invite details.
  if (inviteLoading) {
    return (
      <div className={styles.container}>
        <Skeleton className={styles.loadingCard} />
      </div>
    );
  }

  // Error or invalid invite.
  if (inviteError || !invite) {
    const message = inviteError instanceof ApiError
      ? inviteError.message
      : "This invite is no longer valid.";
    return (
      <div className={styles.container}>
        <div className={styles.card}>
          <span className={styles.logo}>
            <BaguetteLogo size={30} />
          </span>
          <p className={styles.title}>Invite unavailable</p>
          <p className={styles.errorText}>{message}</p>
          <Button variant="secondary" onClick={() => navigate("/bdbs", { replace: true })}>
            Go to my bdbs
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className={styles.container}>
      <div className={styles.card}>
        <span className={styles.logo}>
            <BaguetteLogo size={30} />
          </span>
        <p className={styles.title}>
          You've been invited to join "{invite.bdb_name}"
          {invite.created_by_email && (
            <> by <span className={styles.email}>{invite.created_by_email}</span></>
          )}
        </p>
        {invite.bdb_description && (
          <p className={styles.subtitle}>{invite.bdb_description}</p>
        )}
        <Button onClick={handleAccept} loading={acceptMutation.isPending}>
          Join this bdb
        </Button>
      </div>
    </div>
  );
}
