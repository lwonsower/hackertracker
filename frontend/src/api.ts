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

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { 'content-type': 'application/json', ...init?.headers },
  })

  if (!res.ok) {
    // The API always answers errors as {"error": "..."}; fall back to the
    // status line if something else went wrong (a proxy, say).
    const message = await res
      .json()
      .then((body: { error?: string }) => body.error)
      .catch(() => null)
    throw new Error(message ?? `${res.status} ${res.statusText}`)
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

/**
 * Connects a polling source. The credentials reference is a *pointer* to an
 * environment variable, never the token itself — nothing secret is sent here
 * or stored in the database.
 */
export function connectGitHub(
  label: string,
  credentialsRef: string,
): Promise<{ login: string; source_account: SourceAccount }> {
  return request('/api/source-accounts', {
    method: 'POST',
    body: JSON.stringify({ label, source: 'github', mode: 'pull', credentials_ref: credentialsRef }),
  })
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
