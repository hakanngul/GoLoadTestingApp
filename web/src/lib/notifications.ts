import type { NotificationChannel, NotificationEvent, NotificationKind, NotificationRequest } from '../api/types';

export const EVENT_OPTIONS: { value: NotificationEvent; label: string; hint: string }[] = [
  { value: 'run_finished', label: 'Run finished', hint: 'Every run that reaches a final status, including runs started by a suite' },
  { value: 'suite_finished', label: 'Suite finished', hint: 'A suite run that reaches a final status' },
];

export const KIND_LABEL: Record<NotificationKind, string> = { slack: 'Slack', webhook: 'Webhook' };

/** Editable state of the channel dialog. */
export interface NotificationForm {
  name: string;
  kind: NotificationKind;
  url: string;
  events: NotificationEvent[];
  only_failures: boolean;
  enabled: boolean;
}

export function emptyNotificationForm(): NotificationForm {
  return { name: '', kind: 'slack', url: '', events: ['run_finished', 'suite_finished'], only_failures: false, enabled: true };
}

/** The form for editing c: the URL starts empty (it is never sent to the browser). */
export function channelToForm(c: NotificationChannel): NotificationForm {
  return { name: c.name, kind: c.kind, url: '', events: [...c.events], only_failures: c.only_failures, enabled: c.enabled };
}

/** "Run finished, Suite finished" in canonical order. */
export function describeEvents(events: NotificationEvent[]): string {
  const known = EVENT_OPTIONS.filter((o) => events.includes(o.value)).map((o) => o.label);
  return known.length > 0 ? known.join(', ') : 'none';
}

/** Toggles ev in events, keeping the canonical order. */
export function toggleEvent(events: NotificationEvent[], ev: NotificationEvent): NotificationEvent[] {
  const next = events.includes(ev) ? events.filter((e) => e !== ev) : [...events, ev];
  return EVENT_OPTIONS.map((o) => o.value).filter((v) => next.includes(v));
}

/** Why url is not an acceptable webhook URL, or '' (https; http only for localhost). */
export function urlError(raw: string): string {
  const v = raw.trim();
  let u: URL;
  try {
    u = new URL(v);
  } catch {
    return 'Enter a full URL starting with https://';
  }
  if (u.protocol === 'https:') return '';
  if (u.protocol === 'http:') {
    const h = u.hostname.toLowerCase();
    if (h === 'localhost' || h === '127.0.0.1' || h === '[::1]') return '';
    return 'Use https:// (http is only allowed for localhost)';
  }
  return 'Enter a full URL starting with https://';
}

/**
 * Client-side validation mirroring the server. A blank URL is fine when
 * editing (the stored one is kept). Keys are API field names.
 */
export function validateForm(f: NotificationForm, editing: boolean): Record<string, string> {
  const errors: Record<string, string> = {};
  const name = f.name.trim();
  if (name === '') errors.name = 'Name is required';
  else if ([...name].length > 100) errors.name = 'At most 100 characters';
  if (f.url.trim() === '') {
    if (!editing) errors.url = 'Webhook URL is required';
  } else {
    const e = urlError(f.url);
    if (e) errors.url = e;
  }
  if (f.events.length === 0) errors.events = 'Pick at least one event';
  return errors;
}

export function formToRequest(f: NotificationForm): NotificationRequest {
  return {
    name: f.name.trim(),
    kind: f.kind,
    url: f.url.trim(),
    events: f.events,
    only_failures: f.only_failures,
    enabled: f.enabled,
  };
}

/** Request that flips only the enabled flag of c (URL kept server-side). */
export function toggleRequest(c: NotificationChannel): NotificationRequest {
  return { name: c.name, kind: c.kind, url: '', events: c.events, only_failures: c.only_failures, enabled: !c.enabled };
}
