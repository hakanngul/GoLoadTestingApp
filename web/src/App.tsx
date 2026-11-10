import { NavLink, Route, Routes, useLocation } from 'react-router-dom';
import { api } from './api/client';
import { Icon } from './components/ui';
import { useAsync } from './lib/hooks';
import { ComparePage } from './pages/ComparePage';
import { NotFound } from './pages/NotFound';
import { RunPage } from './pages/RunPage';
import { RunsPage } from './pages/RunsPage';
import { TestEditorPage } from './pages/TestEditorPage';
import { TestsPage } from './pages/TestsPage';
import { WorkersPage } from './pages/WorkersPage';

export function App() {
  const loc = useLocation();
  const health = useAsync(() => api.health(), [], 15000);
  const up = health.data?.status === 'ok';
  return (
    <div className="app">
      <aside className="sidebar">
        <div className="sidebar-inner">
        <div className="brand">
          <span className="brand-mark">
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="#fff" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
              <path d="M2 12l3.5-4 3 2.5L14 4" />
            </svg>
          </span>
          GoLoad
        </div>
        <nav className="nav">
          <NavLink to="/" end className={({ isActive }) => (isActive || loc.pathname.startsWith('/tests') ? 'active' : '')}>
            <Icon name="tests" /> Tests
          </NavLink>
          <NavLink to="/runs">
            <Icon name="runs" /> Runs
          </NavLink>
          <NavLink to="/compare">
            <Icon name="compare" /> Compare
          </NavLink>
          <NavLink to="/workers">
            <Icon name="workers" /> Workers
          </NavLink>
        </nav>
        <div className="sidebar-foot" title={health.error ? 'API unreachable' : 'API reachable'}>
          <span className={`dot ${health.loading ? '' : up ? 'ok' : 'down'}`} />
          {health.loading ? 'connecting…' : up ? `API ${health.data?.version ?? ''}` : 'API unreachable'}
        </div>
        </div>
      </aside>
      <main className="main">
        <Routes>
          <Route path="/" element={<TestsPage />} />
          <Route path="/tests/new" element={<TestEditorPage />} />
          <Route path="/tests/:id" element={<TestEditorPage />} />
          <Route path="/runs" element={<RunsPage />} />
          <Route path="/runs/:id" element={<RunPage />} />
          <Route path="/compare" element={<ComparePage />} />
          <Route path="/workers" element={<WorkersPage />} />
          <Route path="*" element={<NotFound />} />
        </Routes>
      </main>
    </div>
  );
}
