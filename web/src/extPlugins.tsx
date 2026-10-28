/**
 * Plugin extension points for the web app.
 *
 * Exports a default empty plugin. Distributions can override this module to
 * register additional routes, sidebar nav items, and user menu items.
 */

import type { ReactNode } from "react";
import type { RouteObject } from "react-router-dom";
import { SetupGuidePage } from "./pages/Setup/SetupGuidePage";

export interface ExtNavItem {
  /** Path the nav item links to. */
  to: string;
  /** Display label. */
  label: string;
}

/** Describes one link rendered in the app footer (e.g. a legal page). */
export interface ExtFooterLink {
  /** Path the footer link points to. */
  to: string;
  /** Display label. */
  label: string;
}

/** Describes one entry in the TopBar user dropdown menu. */
export type ExtUserMenuItem =
  | { type: "link"; to: string; label: string }
  | {
      type: "action";
      label: string;
      onClick: () => void;
      variant?: "default" | "danger";
    }
  | { type: "header"; render: () => ReactNode }
  | { type: "separator" };

/** Describes an action button rendered on the Home page (e.g. a sign-up link). */
export interface ExtHomeAction {
  label: string;
  to: string;
  variant?: "primary" | "secondary";
}

/** Describes an additional tab on the admin panel page. */
export interface ExtAdminTab {
  /** Unique identifier for the tab. */
  id: string;
  /** Display label shown in the tab bar. */
  label: string;
  /** Content rendered when the tab is active. */
  element: ReactNode;
}

export interface ExtPlugin {
  /** Routes mounted under the protected `<AppLayout>` route tree. */
  routes: RouteObject[];

  /** Public routes mounted outside the auth-protected tree (e.g. registration). */
  publicRoutes?: RouteObject[];

  /** Links appended to the app footer and the public Home footer (e.g. legal pages). */
  footerLinks?: ExtFooterLink[];

  /** Action buttons rendered on the Home page below the Sign In button. */
  homeActions?: ExtHomeAction[];

  /** Additional tabs rendered on the admin panel page. */
  adminTabs?: ExtAdminTab[];

  /** Additional sidebar nav items rendered below the bdbs list. */
  navItems: ExtNavItem[];

  /** Additional items injected into the TopBar user menu. */
  userMenuItems: ExtUserMenuItem[];

  /**
   * Extra license text appended to the Licenses page after the built-in notices.
   * Distributions add their own third-party notices here (each distribution
   * keeps its own canonical text file as the source of truth).
   */
  thirdPartyLicensesAppendix?: string;

  /**
   * Hides the Settings page's Account Storage section — for distributions that surface account
   * storage on a page of their own, so the same meter isn't shown twice.
   */
  hideSettingsAccountStorage?: boolean;
}

/** Default plugin — provides the MCP setup guide as a protected route. */
export const extPlugin: ExtPlugin = {
  routes: [
    { path: "/setup", element: <SetupGuidePage /> },
  ],
  navItems: [
    { to: "/setup", label: "Setup Guide" },
  ],
  userMenuItems: [],
};
