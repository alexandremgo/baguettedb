import { lazy, Suspense } from "react";
import { Routes, Route, Navigate } from "react-router-dom";
import { AppLayout, RequireAuth } from "./components/Layout";
import { Home } from "./pages/Home/Home";
import { LoginCallback } from "./pages/LoginCallback/LoginCallback";
import { SignIn } from "./pages/SignIn/SignIn";
import { InviteAccept } from "./pages/InviteAccept/InviteAccept";
import { Admin } from "./pages/Admin/Admin";
import { BdbList } from "./pages/BdbList/BdbList";
import { BdbCreate } from "./pages/BdbCreate/BdbCreate";
import { BdbNew } from "./pages/BdbNew/BdbNew";
import { BdbDetail } from "./pages/BdbDetail/BdbDetail";
import { Settings } from "./pages/Settings/Settings";
import { StarterGallery } from "./components/StarterGallery";
import { extPlugin } from "./extPlugins";
import {
  StandaloneLayout,
  StandaloneHome,
  FormulasScreen,
  FormulaScreen,
  TablesScreen,
  TableScreen,
} from "./standalone";

// The Licenses page carries the full third-party notices text (a few hundred KB), so it loads as its own chunk on
// visit instead of weighing on every page load.
const Licenses = lazy(() => import("./pages/Licenses/Licenses").then((m) => ({ default: m.Licenses })));

export function AppRoutes() {
  return (
    <Routes>
      {/* Public routes */}
      <Route path="/" element={<Home />} />
      <Route path="/callback" element={<LoginCallback />} />
      <Route path="/sign-in" element={<SignIn />} />
      <Route path="/invite/:token" element={<InviteAccept />} />
      <Route
        path="/licenses"
        element={
          <Suspense fallback={null}>
            <Licenses />
          </Suspense>
        }
      />

      {/* Plugin-provided public routes (e.g. registration in downstream distributions). */}
      {extPlugin.publicRoutes?.map((r, i) => (
        <Route key={`plugin-public-${i}`} path={r.path} element={r.element} />
      ))}

      {/* Protected routes with app layout */}
      <Route element={<RequireAuth />}>
        <Route element={<AppLayout />}>
          <Route path="/bdbs" element={<BdbList />} />
          <Route path="/bdbs/new" element={<BdbNew />} />
          <Route path="/bdbs/new/blank" element={<BdbCreate />} />
          <Route path="/bdbs/:id" element={<BdbDetail />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="/admin" element={<Admin />} />
          {/* Additional routes injected by the plugin. */}
          {extPlugin.routes.map((r, i) => (
            <Route key={`plugin-${i}`} path={r.path} element={r.element} />
          ))}
        </Route>
      </Route>

      {/* Standalone shell — a parallel protected tree for the installed/standalone PWA experience. */}
      <Route element={<RequireAuth />}>
        <Route element={<StandaloneLayout />}>
          <Route path="/app" element={<Navigate to="/app/bdbs" replace />} />
          <Route path="/app/bdbs" element={<StandaloneHome />} />
          {/* Create flow stays inside the standalone shell: same gallery + blank form, /app-scoped navigation. */}
          <Route
            path="/app/bdbs/new"
            element={
              <StarterGallery
                blankTo="/app/bdbs/new/blank"
                createdTo={(bdb) => `/app/bdbs/${bdb.id}/formulas`}
              />
            }
          />
          <Route
            path="/app/bdbs/new/blank"
            element={<BdbCreate createdTo={(bdb) => `/app/bdbs/${bdb.id}/formulas`} />}
          />
          <Route path="/app/bdbs/:bdbId/formulas" element={<FormulasScreen />} />
          <Route path="/app/bdbs/:bdbId/formulas/:formulaId" element={<FormulaScreen />} />
          <Route path="/app/bdbs/:bdbId/tables" element={<TablesScreen />} />
          <Route path="/app/bdbs/:bdbId/tables/:table" element={<TableScreen />} />
        </Route>
      </Route>
    </Routes>
  );
}
