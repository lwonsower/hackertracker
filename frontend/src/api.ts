// Thin wrapper over the backend. Vite proxies /api to the Go server in dev, so
// there is one origin and no CORS anywhere.

export type EventRow = {
  id: string
  external_id: string
  kind: string
  subject_key?: string
  title: string
  url?: string
  occurred_at: string
  payload?: Record<string, unknown>
  source: string
  source_label: string
}

export type ManualEntry = {
  title: string
  kind: string
  occurred_at: string
  url?: string
  note?: string
}

/** Thrown on a 401 so callers can distinguish "signed out" from a real error. */
export class NotSignedIn extends Error {
  constructor() {
    super('not signed in')
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { 'content-type': 'application/json', ...init?.headers },
  })

  if (res.status === 401) {
    throw new NotSignedIn()
  }

  if (!res.ok) {
    // The API always answers errors as {"error": "..."}; fall back to the
    // status line if something else went wrong (a proxy, say).
    const message = await res
      .json()
      .then((body: { error?: string }) => body.error)
      .catch(() => null)
    throw new Error(message ?? `${res.status} ${res.statusText}`)
  }

  // A 204 carries no body, and asking an empty one for JSON throws.
  if (res.status === 204) {
    return undefined as T
  }

  return res.json() as Promise<T>
}

export function listEvents(from: string, to: string): Promise<{ events: EventRow[] }> {
  const params = new URLSearchParams({ from, to, limit: '250' })
  return request(`/api/events?${params}`)
}

export function createEvent(entry: ManualEntry): Promise<unknown> {
  return request('/api/events', { method: 'POST', body: JSON.stringify(entry) })
}

export type SourceAccount = {
  id: string
  source: string
  mode: string
  label: string
  external_account_id?: string
  credentials_ref?: string
  last_synced_at?: string
  last_error?: string
}

export type SyncReport = {
  source_account_id: string
  label: string
  since: string
  rounds: number
  complete: boolean
  examined?: Record<string, number>
  created: number
  updated: number
  notes?: string[]
}

export function listSources(): Promise<{ source_accounts: SourceAccount[] }> {
  return request('/api/source-accounts')
}

export type Credential = {
  id: string
  label: string
  created_at: string
  last_used_at?: string
}

/**
 * Connects a polling source. Either a token, which is encrypted and stored per
 * account, or a credentials reference (env:NAME) on self-hosted servers.
 */
export function connectGitHub(
  label: string,
  auth: { token: string } | { credentialsRef: string },
): Promise<{ login: string; source_account: SourceAccount }> {
  return request('/api/source-accounts', {
    method: 'POST',
    body: JSON.stringify({
      label,
      source: 'github',
      mode: 'pull',
      ...('token' in auth ? { token: auth.token } : { credentials_ref: auth.credentialsRef }),
    }),
  })
}

export function listCredentials(): Promise<{ credentials: Credential[] }> {
  return request('/api/credentials')
}

export function deleteCredential(id: string): Promise<void> {
  return request(`/api/credentials/${id}`, { method: 'DELETE' }) as Promise<void>
}

/**
 * Runs a sync. `since` (YYYY-MM-DD) overrides where the backfill starts, which
 * is how you reach history older than the point a previous successful sync
 * already advanced the watermark past.
 */
export function syncSource(id: string, since?: string): Promise<SyncReport> {
  return request(`/api/source-accounts/${id}/sync`, {
    method: 'POST',
    body: JSON.stringify(since ? { since } : {}),
  })
}

export type Arc = {
  id: string
  title: string
  status: 'open' | 'done' | 'dropped'
  summary?: string
  started_at?: string
  target_at?: string
  ended_at?: string
  created_at: string
}

export type ArcRow = Arc & {
  event_count: number
  entry_count: number
  last_event_at?: string
}

export type ArcEntry = {
  id: string
  arc_id: string
  kind?: string
  body: string
  occurred_at: string
  created_at: string
}

/** An event plus the other arcs it is already filed under. */
export type ArcEvent = EventRow & { other_arcs?: string[] }

export type ArcInput = {
  title: string
  status: string
  summary?: string
  started_at?: string
  target_at?: string
  ended_at?: string
}

export const ENTRY_KINDS = ['hypothesis', 'update', 'risk', 'outcome', 'retro'] as const

export function listArcs(): Promise<{ arcs: ArcRow[] }> {
  return request('/api/arcs')
}

export function createArc(input: ArcInput): Promise<{ arc: Arc }> {
  return request('/api/arcs', { method: 'POST', body: JSON.stringify(input) })
}

export function getArc(id: string): Promise<{ arc: Arc; entries: ArcEntry[]; events: ArcEvent[] }> {
  return request(`/api/arcs/${id}`)
}

/** A whole-record replace, so clearing a date is expressible. */
export function updateArc(id: string, input: ArcInput): Promise<{ arc: Arc }> {
  return request(`/api/arcs/${id}`, { method: 'PUT', body: JSON.stringify(input) })
}

export function addArcEntry(
  id: string,
  entry: { kind?: string; body: string; occurred_at?: string },
): Promise<{ entry: ArcEntry }> {
  return request(`/api/arcs/${id}/entries`, { method: 'POST', body: JSON.stringify(entry) })
}

/** Events not yet filed under this arc, narrowed by the search box. */
export function arcCandidates(
  id: string,
  opts: { q?: string; from?: string; to?: string } = {},
): Promise<{ events: ArcEvent[] }> {
  const params = new URLSearchParams({ limit: '100' })
  if (opts.q) params.set('q', opts.q)
  if (opts.from) params.set('from', opts.from)
  if (opts.to) params.set('to', opts.to)
  return request(`/api/arcs/${id}/candidates?${params}`)
}

/** Files many events at once and returns the arc's evidence afterwards. */
export function attachEvents(
  id: string,
  eventIds: string[],
): Promise<{ attached: number; events: ArcEvent[] }> {
  return request(`/api/arcs/${id}/events`, {
    method: 'POST',
    body: JSON.stringify({ event_ids: eventIds }),
  })
}

/** Unfiles an event. The event itself stays in the capture layer. */
export function detachEvent(id: string, eventId: string): Promise<void> {
  return request(`/api/arcs/${id}/events/${eventId}`, { method: 'DELETE' }) as Promise<void>
}

export type Me = {
  id: string
  account_id: string
  email: string
  name?: string
  avatar_url?: string
}

export type Providers = { google: boolean; dev: boolean; self_host: boolean }

export function me(): Promise<{ user: Me }> {
  return request('/api/me')
}

export function authProviders(): Promise<Providers> {
  return request('/api/auth/providers')
}

/** Only available on self-hosted instances with DEV_SIGN_IN_EMAIL set. */
export function devSignIn(): Promise<{ user: Me }> {
  return request('/api/auth/dev-signin', { method: 'POST' })
}

export async function signOut(): Promise<void> {
  await fetch('/api/auth/signout', { method: 'POST' })
}
