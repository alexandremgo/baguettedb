import { Navigate, useNavigate, Link } from "react-router-dom";
import { useAuth } from "../../auth";
import { isTokenMode } from "../../config";
import { isStandalone } from "../../standalone/isStandalone";
import { BaguetteLogo } from "../../components/BaguetteLogo";
import { Button } from "../../components/Button";
import { Skeleton } from "../../components/Skeleton";
import { extPlugin } from "../../extPlugins";
import styles from "./Home.module.css";

export function Home() {
  const { isAuthenticated, isLoading, login } = useAuth();
  const navigate = useNavigate();

  if (isLoading) {
    return (
      <div className={styles.container}>
        <Skeleton className={styles.loadingCard} />
      </div>
    );
  }

  if (isAuthenticated) {
    return <Navigate to={isStandalone() ? "/app/bdbs" : "/bdbs"} replace />;
  }

  // Under `bdb serve` the sign-in page is the only way in, so it opens at once instead of behind a button.
  if (isTokenMode()) {
    return <Navigate to="/sign-in" replace />;
  }

  return (
    <div className={styles.container}>
      <div className={styles.card}>
        <div className={styles.logo}>
          <BaguetteLogo size={64} />
        </div>
        <h1 className={styles.title}>BaguetteDB</h1>
        <p className={styles.subtitle}>Your data, shared your way</p>
        <div className={styles.actions}>
          <Button onClick={() => login()}>Sign In</Button>
          {extPlugin.homeActions?.map((action, i) => (
            <Button
              key={`home-action-${i}`}
              variant={action.variant ?? "secondary"}
              onClick={() => navigate(action.to)}
            >
              {action.label}
            </Button>
          ))}
        </div>
      </div>
      <footer className={styles.footer}>
        <span>© {new Date().getFullYear()} BaguetteDB. All rights reserved.</span>
        {extPlugin.footerLinks?.map((link, i) => (
          <Link key={`footer-${i}`} to={link.to} className={styles.footerLink}>
            {link.label}
          </Link>
        ))}
      </footer>
    </div>
  );
}
