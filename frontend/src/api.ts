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
