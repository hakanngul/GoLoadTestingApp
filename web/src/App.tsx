import { lazy, Suspense, useCallback, useEffect, useState } from 'react';
import { NavLink, Route, Routes, useLocation } from 'react-router-dom';
import { api, clearToken, getToken, onUnauthorized } from './api/client';
import { ThemeToggle } from './components/ThemeToggle';
import { BrandMark, Icon, Loading } from './components/ui';
import { useAsync } from './lib/hooks';
import { NotFound } from './pages/NotFound';
import { SignInPage } from './pages/SignInPage';

// Route-level code splitting: recharts (Run, Compare, editor profile preview)
// and the YAML parser load only with the pages that need them.
const TestsPage = lazy(() => import('./pages/TestsPage').then((m) => ({ default: m.TestsPage })));
const TestEditorPage = lazy(() => import('./pages/TestEditorPage').then((m) => ({ default: m.TestEditorPage })));
const RunsPage = lazy(() => import('./pages/RunsPage').then((m) => ({ default: m.RunsPage })));
const RunPage = lazy(() => import('./pages/RunPage').then((m) => ({ default: m.RunPage })));
const ComparePage = lazy(() => import('./pages/ComparePage').then((m) => ({ default: m.ComparePage })));
const ReportsPage = lazy(() => import('./pages/ReportsPage').then((m) => ({ default: m.ReportsPage })));
const TestReportPage = lazy(() => import('./pages/TestReportPage').then((m) => ({ default: m.TestReportPage })));
const SchedulesPage = lazy(() => import('./pages/SchedulesPage').then((m) => ({ default: m.SchedulesPage })));
const WorkersPage = lazy(() => import('./pages/WorkersPage').then((m) => ({ default: m.WorkersPage })));

/**
 * Auth gate: any API call answered with 401 swaps the whole UI for the
 * sign-in screen. After a successful sign-in the shell is remounted (new
 * `key`) so every page reloads its data with the new token.
 */
export function App() {
  const [needAuth, setNeedAuth] = useState(false);
  const [epoch, setEpoch] = useState(0);
  const [hasToken, setHasToken] = useState(() => getToken() !== null);

  useEffect(() => onUnauthorized(() => setNeedAuth(true)), []);

  const signedIn = useCallback(() => {
    setHasToken(true);
    setNeedAuth(false);
    setEpoch((e) => e + 1);
  }, []);
  const signOut = useCallback(() => {
    clearToken();
    setHasToken(false);
    setNeedAuth(true);
  }, []);

  if (needAuth) return <SignInPage onSignedIn={signedIn} />;
  return <Shell key={epoch} hasToken={hasToken} onSignOut={signOut} />;
}

function Shell({ hasToken, onSignOut }: { hasToken: boolean; onSignOut: () => void }) {
  const loc = useLocation();
  const health = useAsync(() => api.health(), [], 15000);
  const up = health.data?.status === 'ok';
  return (
    <div className="app">
      <aside className="sidebar no-print">
        <div className="sidebar-inner">
          <div className="brand">
            <BrandMark />
            GoLoad
          </div>
          <nav className="nav">
            <NavLink to="/" end className={({ isActive }) => (isActive || loc.pathname.startsWith('/tests') ? 'active' : '')}>
              <Icon name="tests" /> Tests
            </NavLink>
            <NavLink to="/runs">
              <Icon name="runs" /> Runs
            </NavLink>
            <NavLink to="/schedules">
              <Icon name="calendar" /> Schedules
            </NavLink>
            <NavLink to="/reports">
              <Icon name="reports" /> Reports
            </NavLink>
            <NavLink to="/compare">
              <Icon name="compare" /> Compare
            </NavLink>
            <NavLink to="/workers">
              <Icon name="workers" /> Workers
            </NavLink>
          </nav>
          <div className="sidebar-bottom">
            {hasToken && (
              <button className="nav-btn" onClick={onSignOut}>
                <Icon name="logout" /> Sign out
              </button>
            )}
            <div className="sidebar-foot" title={health.error ? 'API unreachable' : 'API reachable'}>
              <span className={`dot ${health.loading ? '' : up ? 'ok' : 'down'}`} />
              <span className="sidebar-foot-text">
                {health.loading ? 'connecting…' : up ? `API ${health.data?.version ?? ''}` : 'API unreachable'}
              </span>
              <span className="grow" />
              <ThemeToggle />
            </div>
          </div>
        </div>
      </aside>
      <main className="main">
        <Suspense fallback={<Loading />}>
          <Routes>
            <Route path="/" element={<TestsPage />} />
            <Route path="/tests/new" element={<TestEditorPage />} />
            <Route path="/tests/:id" element={<TestEditorPage />} />
            <Route path="/runs" element={<RunsPage />} />
            <Route path="/runs/:id" element={<RunPage />} />
            <Route path="/schedules" element={<SchedulesPage />} />
            <Route path="/reports" element={<ReportsPage />} />
            <Route path="/reports/tests/:id" element={<TestReportPage />} />
            <Route path="/compare" element={<ComparePage />} />
            <Route path="/workers" element={<WorkersPage />} />
            <Route path="*" element={<NotFound />} />
          </Routes>
        </Suspense>
      </main>
    </div>
  );
}
