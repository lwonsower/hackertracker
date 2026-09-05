import { useCallback, useEffect, useMemo, useState } from 'react'

import { createEvent, listEvents, type EventRow } from '../api'

// Suggestions, not a fixed list — the input stays free text. The ones that
// matter most here are the kinds no API will ever hand you.
const KIND_SUGGESTIONS = [
  'note',
  'mentoring',
  'design_review',
  'doc_published',
  'incident_resolved',
  'talk_given',
  'decision',
]

/** Value for <input type="datetime-local">, which wants local time, no zone. */
function toLocalInputValue(date: Date): string {
  const offset = date.getTimezoneOffset() * 60_000
  return new Date(date.getTime() - offset).toISOString().slice(0, 16)
}

function dayKey(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, {
    weekday: 'long',
    year: 'numeric',
    month: 'long',
    day: 'numeric',
  })
}

function timeLabel(iso: string): string {
  return new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
}

export default function Home() {
  const [events, setEvents] = useState<EventRow[]>([])
  const [loadError, setLoadError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  const [title, setTitle] = useState('')
  const [kind, setKind] = useState('note')
  const [occurredAt, setOccurredAt] = useState(() => toLocalInputValue(new Date()))
  const [url, setUrl] = useState('')
  const [note, setNote] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)

  // A wide default window: this is a record of work, so the useful question is
  // "what happened this year", not "what happened today".
  const window = useMemo(() => {
    const to = new Date()
    to.setDate(to.getDate() + 1)
    const from = new Date()
    from.setFullYear(from.getFullYear() - 1)
    return { from: from.toISOString().slice(0, 10), to: to.toISOString().slice(0, 10) }
  }, [])

  const refresh = useCallback(async () => {
    try {
      const { events } = await listEvents(window.from, window.to)
      setEvents(events)
      setLoadError(null)
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : String(err))
    } finally {
      setLoading(false)
    }
  }, [window])

  useEffect(() => {
    void refresh()
  }, [refresh])

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setSaving(true)
    setSaveError(null)
    try {
      await createEvent({
        title,
        kind: kind.trim() || 'note',
        // datetime-local has no zone; the Date round-trip attaches the
        // browser's, which is what the user meant.
        occurred_at: new Date(occurredAt).toISOString(),
        url: url.trim() || undefined,
        note: note.trim() || undefined,
      })
      setTitle('')
      setUrl('')
      setNote('')
      setOccurredAt(toLocalInputValue(new Date()))
      await refresh()
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  const grouped = useMemo(() => {
    const days = new Map<string, EventRow[]>()
    for (const event of events) {
      const key = dayKey(event.occurred_at)
      const bucket = days.get(key)
      if (bucket) bucket.push(event)
      else days.set(key, [event])
    }
    return [...days.entries()]
  }, [events])

  return (
    <div className="page">
      <header className="page__header">
        <h1 className="wordmark">hacker tracker</h1>
        <p className="page__tagline">A record of what you actually did.</p>
      </header>

      <div className="layout">
        <section className="panel" aria-labelledby="capture-heading">
          <h2 id="capture-heading" className="panel__title">
            Capture
          </h2>
          <p className="panel__hint">
            The work that never reaches an API — mentoring, design review, the call you
            talked someone out of — only gets recorded if you write it down.
          </p>

          <form className="form" onSubmit={handleSubmit}>
            <label className="field">
              <span className="field__label">What happened</span>
              <input
                className="field__input"
                value={title}
                onChange={(e) => setTitle(e.target.value)}
                placeholder="Unblocked Jordan on the rollback plan"
                required
              />
            </label>

            <div className="field-row">
              <label className="field">
                <span className="field__label">Kind</span>
                <input
                  className="field__input"
                  value={kind}
                  onChange={(e) => setKind(e.target.value)}
                  list="kind-suggestions"
                />
                <datalist id="kind-suggestions">
                  {KIND_SUGGESTIONS.map((k) => (
                    <option key={k} value={k} />
                  ))}
                </datalist>
              </label>

              <label className="field">
                <span className="field__label">When</span>
                <input
                  className="field__input"
                  type="datetime-local"
                  value={occurredAt}
                  onChange={(e) => setOccurredAt(e.target.value)}
                  required
                />
              </label>
            </div>

            <label className="field">
              <span className="field__label">
                Link <span className="field__optional">optional</span>
              </span>
              <input
                className="field__input"
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                placeholder="https://"
              />
            </label>

            <label className="field">
              <span className="field__label">
                Context <span className="field__optional">optional</span>
              </span>
              <textarea
                className="field__input field__input--area"
                value={note}
                onChange={(e) => setNote(e.target.value)}
                rows={3}
                placeholder="Why it mattered, who it helped, what it unblocked."
              />
            </label>

            {saveError && <p className="alert">{saveError}</p>}

            <button className="button" type="submit" disabled={saving || !title.trim()}>
              {saving ? 'Saving…' : 'Record it'}
            </button>
          </form>
        </section>

        <section className="panel" aria-labelledby="timeline-heading">
          <h2 id="timeline-heading" className="panel__title">
            Timeline
            {events.length > 0 && <span className="counter">{events.length}</span>}
          </h2>

          {loading && <p className="panel__hint">Loading…</p>}
          {loadError && <p className="alert">{loadError}</p>}

          {!loading && !loadError && events.length === 0 && (
            <p className="panel__hint">
              Nothing recorded yet. Add something on the left, or post to an ingest
              endpoint from a script.
            </p>
          )}

          <ol className="timeline">
            {grouped.map(([day, dayEvents]) => (
              <li key={day} className="timeline__day">
                <h3 className="timeline__date">{day}</h3>
                <ul className="timeline__events">
                  {dayEvents.map((event) => (
                    <li key={event.id} className="event">
                      <div className="event__meta">
                        <span className="badge">{event.kind}</span>
                        <span className="event__time">{timeLabel(event.occurred_at)}</span>
                        <span className="event__source">{event.source_label}</span>
                      </div>
                      <p className="event__title">
                        {event.url ? (
                          <a className="event__link" href={event.url} target="_blank" rel="noreferrer">
                            {event.title}
                          </a>
                        ) : (
                          event.title
                        )}
                      </p>
                      {typeof event.payload?.note === 'string' && (
                        <p className="event__note">{event.payload.note}</p>
                      )}
                    </li>
                  ))}
                </ul>
              </li>
            ))}
          </ol>
        </section>
      </div>
    </div>
  )
}
