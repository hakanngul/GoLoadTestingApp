import { createContext, useContext, type ReactNode } from 'react';
import { api } from '../api/client';
import type { Me } from '../api/types';
import { useAsync } from './hooks';
import { scopeAllows, type Scope } from './tokens';

export interface MeState {
  /** undefined while loading or when /api/me is unavailable (older server). */
  me: Me | undefined;
  loading: boolean;
}

const MeContext = createContext<MeState>({ me: undefined, loading: true });

/** Fetches /api/me once for the whole shell. */
export function MeProvider({ children }: { children: ReactNode }) {
  const r = useAsync(() => api.getMe(), []);
  return <MeContext.Provider value={{ me: r.data, loading: r.loading }}>{children}</MeContext.Provider>;
}

export function useMe(): MeState {
  return useContext(MeContext);
}

/** True when the caller has at least `need`; unknown identity (old server) counts as allowed. */
export function useCan(need: Scope): boolean {
  const { me, loading } = useMe();
  if (loading || !me) return true;
  return scopeAllows(me.scope, need);
}
