/** Redirects to home if not authenticated. Wraps protected routes. */

import { Navigate, Outlet } from "react-router-dom";
import { useAuth } from "../../auth";
import { SkeletonRows } from "../Skeleton";
import styles from "./RequireAuth.module.css";

export function RequireAuth() {
  const { isAuthenticated, isLoading } = useAuth();

  if (isLoading) {
    return (
      <div className={styles.loading}>
        <SkeletonRows />
      </div>
    );
  }

  if (!isAuthenticated) {
    return <Navigate to="/" replace />;
  }

  return <Outlet />;
}
