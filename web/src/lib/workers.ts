import type { Worker } from '../api/types';

/** The master always lists its in-process pseudo-worker as `local` (addr `in-process`). */
export function isMasterWorker(w: Worker): boolean {
  return w.id === 'local' || w.addr === 'in-process';
}

/** Remote workers (everything except the master's in-process pseudo-worker). */
export function remoteWorkers(list: Worker[]): Worker[] {
  return list.filter((w) => !isMasterWorker(w));
}

export function idleRemoteCount(list: Worker[]): number {
  return remoteWorkers(list).filter((w) => w.state === 'idle').length;
}
