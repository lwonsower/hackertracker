import { useCallback, useEffect, useMemo, useState } from 'react'
import { useOutletContext } from 'react-router-dom'

import { listEvents, type EventRow } from '../api'
import ReviewBanner from '../components/ReviewBanner'
import type { ShellContext } from '../layout/AppShell'

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

export default function Timeline() {
  const [events, setEvents] = useState<EventRow[]>([])
  const [loadError, setLoadError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  // Capture happens in the sidebar, on whatever page you are on, so the count
  // of saves is what tells this page it is out of date.
  const { captures } = useOutletContext<ShellContext>()

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
  }, [refresh, captures])

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
    <>
      <h1 className="page__title">Timeline</h1>

      <ReviewBanner onAdded={() => void refresh()} />

      <section className="panel" aria-labelledby="timeline-heading">
        <h2 id="timeline-heading" className="panel__title">
          The last year
          {events.length > 0 && <span className="counter">{events.length}</span>}
        </h2>

        {loading && <p className="panel__hint">Loading…</p>}
        {loadError && <p className="alert">{loadError}</p>}

        {!loading && !loadError && events.length === 0 && (
          <p className="panel__hint">
            Nothing recorded yet. Hit <strong>Record something</strong> in the sidebar,
            connect a source, or post to an ingest endpoint from a script.
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
    </>
  )
}
