import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'

import {
  addArcEntry,
  arcCandidates,
  attachEvents,
  detachEvent,
  getArc,
  updateArc,
  ENTRY_KINDS,
  type Arc,
  type ArcEntry,
  type ArcEvent,
} from '../api'

function dayLabel(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  })
}

/** Value for <input type="date">, which wants the local calendar day. */
function toDateInputValue(date: Date): string {
  const offset = date.getTimezoneOffset() * 60_000
  return new Date(date.getTime() - offset).toISOString().slice(0, 10)
}

function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

export default function ArcPage() {
  const { id = '' } = useParams()

  const [arc, setArc] = useState<Arc | null>(null)
  const [entries, setEntries] = useState<ArcEntry[]>([])
  const [events, setEvents] = useState<ArcEvent[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      const { arc, entries, events } = await getArc(id)
      setArc(arc)
      setEntries(entries)
      setEvents(events)
      setLoadError(null)
    } catch (err) {
      setLoadError(message(err))
    } finally {
      setLoading(false)
    }
  }, [id])

  useEffect(() => {
    void load()
  }, [load])

  if (loading) return <p className="panel__hint">Loading…</p>
  if (loadError) return <p className="alert">{loadError}</p>
  if (!arc) return null

  return (
    <>
      <Link className="crumb" to="/arcs">
        ← Arcs
      </Link>

      <Header arc={arc} onSaved={setArc} />

      <div className="layout layout--even">
        <Narrative arcId={id} entries={entries} onAdded={(e) => setEntries((all) => [...all, e])} />
        <Evidence arcId={id} events={events} onChanged={setEvents} />
      </div>
    </>
  )
}

// ── header ───────────────────────────────────────────────────────────────

function Header({ arc, onSaved }: { arc: Arc; onSaved: (a: Arc) => void }) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(arc)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  function open() {
    setDraft(arc)
    setError(null)
    setEditing(true)
  }

  async function save(e: React.FormEvent) {
    e.preventDefault()
    setSaving(true)
    setError(null)
    try {
      // A whole-record replace: an emptied date field means "clear it", which
      // a merge could never express.
      const { arc: saved } = await updateArc(arc.id, {
        title: draft.title,
        status: draft.status,
        summary: draft.summary ?? '',
        started_at: draft.started_at ?? '',
        target_at: draft.target_at ?? '',
        ended_at: draft.ended_at ?? '',
      })
      onSaved(saved)
      setEditing(false)
    } catch (err) {
      setError(message(err))
    } finally {
      setSaving(false)
    }
  }

  if (!editing) {
    return (
      <section className="panel arc-head">
        <div className="arc-head__top">
          <h1 className="page__title arc-head__title">{arc.title}</h1>
          <button className="button button--quiet" onClick={open}>
            Edit
          </button>
        </div>

        <div className="arc-card__meta">
          <span className={`badge badge--${arc.status}`}>{arc.status}</span>
          {arc.started_at && <span className="arc-card__stat">from {arc.started_at}</span>}
          {arc.target_at && <span className="arc-card__stat">target {arc.target_at}</span>}
          {arc.ended_at && <span className="arc-card__stat">ended {arc.ended_at}</span>}
        </div>

        {arc.summary && <p className="arc-head__summary">{arc.summary}</p>}
      </section>
    )
  }

  return (
    <section className="panel arc-head">
      <form className="form arc-head__form" onSubmit={save}>
        <label className="field">
          <span className="field__label">Title</span>
          <input
            className="field__input"
            value={draft.title}
            onChange={(e) => setDraft({ ...draft, title: e.target.value })}
            required
          />
        </label>

        <div className="field-row">
          <label className="field">
            <span className="field__label">Status</span>
            <select
              className="field__input"
              value={draft.status}
              onChange={(e) => setDraft({ ...draft, status: e.target.value as Arc['status'] })}
            >
              <option value="open">open</option>
              <option value="done">done</option>
              <option value="dropped">dropped</option>
            </select>
          </label>

          <label className="field">
            <span className="field__label">Started</span>
            <input
              className="field__input"
              type="date"
              value={draft.started_at ?? ''}
              onChange={(e) => setDraft({ ...draft, started_at: e.target.value })}
            />
          </label>

          <label className="field">
            <span className="field__label">Target</span>
            <input
              className="field__input"
              type="date"
              value={draft.target_at ?? ''}
              onChange={(e) => setDraft({ ...draft, target_at: e.target.value })}
            />
          </label>

          <label className="field">
            <span className="field__label">Ended</span>
            <input
              className="field__input"
              type="date"
              value={draft.ended_at ?? ''}
              onChange={(e) => setDraft({ ...draft, ended_at: e.target.value })}
            />
          </label>
        </div>

        <label className="field">
          <span className="field__label">What it is</span>
          <textarea
            className="field__input field__input--area"
            rows={3}
            value={draft.summary ?? ''}
            onChange={(e) => setDraft({ ...draft, summary: e.target.value })}
          />
        </label>

        {error && <p className="alert">{error}</p>}

        <div className="arc-head__actions">
          <button className="button" type="submit" disabled={saving || !draft.title.trim()}>
            {saving ? 'Saving…' : 'Save'}
          </button>
          <button className="button button--quiet" type="button" onClick={() => setEditing(false)}>
            Cancel
          </button>
        </div>
      </form>
    </section>
  )
}

// ── narrative ────────────────────────────────────────────────────────────

function Narrative({
  arcId,
  entries,
  onAdded,
}: {
  arcId: string
  entries: ArcEntry[]
  onAdded: (entry: ArcEntry) => void
}) {
  const [body, setBody] = useState('')
  const [kind, setKind] = useState('')
  const [occurredAt, setOccurredAt] = useState(() => toDateInputValue(new Date()))
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setSaving(true)
    setError(null)
    try {
      const { entry } = await addArcEntry(arcId, {
        kind: kind || undefined,
        body,
        occurred_at: new Date(occurredAt).toISOString(),
      })
      onAdded(entry)
      setBody('')
      setKind('')
      setOccurredAt(toDateInputValue(new Date()))
    } catch (err) {
      setError(message(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <section className="panel" aria-labelledby="narrative-heading">
      <h2 id="narrative-heading" className="panel__title">
        Narrative
        {entries.length > 0 && <span className="counter">{entries.length}</span>}
      </h2>
      <p className="panel__hint">
        Dated and append-only. A hypothesis written before the outcome is evidence of
        judgement; the same sentence written afterwards is not.
      </p>

      <ol className="entries">
        {entries.map((entry) => (
          <li key={entry.id} className="entry">
            <div className="entry__meta">
              <span className="entry__date">{dayLabel(entry.occurred_at)}</span>
              {entry.kind && <span className="badge">{entry.kind}</span>}
            </div>
            <p className="entry__body">{entry.body}</p>
          </li>
        ))}
      </ol>

      <form className="form" onSubmit={submit}>
        <label className="field">
          <span className="field__label">Add to the log</span>
          <textarea
            className="field__input field__input--area"
            rows={3}
            value={body}
            onChange={(e) => setBody(e.target.value)}
            placeholder="I think the cutover fails on the session store. Betting we lose a week."
            required
          />
        </label>

        <div className="field-row">
          <label className="field">
            <span className="field__label">
              Kind <span className="field__optional">optional</span>
            </span>
            <select
              className="field__input"
              value={kind}
              onChange={(e) => setKind(e.target.value)}
            >
              <option value="">none</option>
              {ENTRY_KINDS.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </select>
          </label>

          <label className="field">
            <span className="field__label">When</span>
            <input
              className="field__input"
              type="date"
              value={occurredAt}
              onChange={(e) => setOccurredAt(e.target.value)}
              required
            />
          </label>
        </div>

        {error && <p className="alert">{error}</p>}

        <button className="button" type="submit" disabled={saving || !body.trim()}>
          {saving ? 'Saving…' : 'Add entry'}
        </button>
      </form>
    </section>
  )
}

// ── evidence ─────────────────────────────────────────────────────────────

function Evidence({
  arcId,
  events,
  onChanged,
}: {
  arcId: string
  events: ArcEvent[]
  onChanged: (events: ArcEvent[]) => void
}) {
  const [adding, setAdding] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function detach(eventId: string) {
    setError(null)
    try {
      await detachEvent(arcId, eventId)
      onChanged(events.filter((e) => e.id !== eventId))
    } catch (err) {
      setError(message(err))
    }
  }

  return (
    <section className="panel" aria-labelledby="evidence-heading">
      <h2 id="evidence-heading" className="panel__title">
        Evidence
        {events.length > 0 && <span className="counter">{events.length}</span>}
        <button
          className="button button--quiet panel__action"
          onClick={() => setAdding((open) => !open)}
        >
          {adding ? 'Done' : 'Add evidence'}
        </button>
      </h2>

      {adding && (
        <AddEvidence
          arcId={arcId}
          onAttached={(events) => onChanged(events)}
        />
      )}

      {error && <p className="alert">{error}</p>}

      {events.length === 0 && !adding && (
        <p className="panel__hint">
          Nothing filed yet. Search your events and tick everything that belongs to this
          arc — you file a line of work once, not an event at a time.
        </p>
      )}

      <ul className="evidence">
        {events.map((event) => (
          <li key={event.id} className="event">
            <div className="event__meta">
              <span className="badge">{event.kind}</span>
              <span className="event__time">{dayLabel(event.occurred_at)}</span>
              <span className="event__source">{event.source_label}</span>
              <button className="link-button" onClick={() => void detach(event.id)}>
                Unfile
              </button>
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
            {event.other_arcs && event.other_arcs.length > 0 && (
              <p className="event__note">also in {event.other_arcs.join(', ')}</p>
            )}
          </li>
        ))}
      </ul>
    </section>
  )
}

// ── add evidence ─────────────────────────────────────────────────────────

function AddEvidence({
  arcId,
  onAttached,
}: {
  arcId: string
  onAttached: (events: ArcEvent[]) => void
}) {
  const [query, setQuery] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [candidates, setCandidates] = useState<ArcEvent[]>([])
  const [picked, setPicked] = useState<Set<string>>(() => new Set())
  const [searching, setSearching] = useState(false)
  const [attaching, setAttaching] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [attached, setAttached] = useState<number | null>(null)

  // A generation counter, so a slow early response cannot overwrite the
  // results of a later, narrower search.
  const generation = useRef(0)

  useEffect(() => {
    const run = ++generation.current
    const timer = setTimeout(async () => {
      setSearching(true)
      try {
        const { events } = await arcCandidates(arcId, {
          q: query.trim() || undefined,
          from: from || undefined,
          to: to || undefined,
        })
        if (generation.current !== run) return
        setCandidates(events)
        setError(null)
      } catch (err) {
        if (generation.current === run) setError(message(err))
      } finally {
        if (generation.current === run) setSearching(false)
      }
    }, 250)
    return () => clearTimeout(timer)
  }, [arcId, query, from, to])

  const visibleIds = useMemo(() => candidates.map((c) => c.id), [candidates])
  const allPicked = visibleIds.length > 0 && visibleIds.every((id) => picked.has(id))

  function toggle(id: string) {
    setPicked((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  function toggleAll() {
    setPicked((current) => {
      const next = new Set(current)
      if (allPicked) visibleIds.forEach((id) => next.delete(id))
      else visibleIds.forEach((id) => next.add(id))
      return next
    })
  }

  async function attach() {
    setAttaching(true)
    setError(null)
    try {
      const { attached, events } = await attachEvents(arcId, [...picked])
      onAttached(events)
      setAttached(attached)
      setPicked(new Set())
      // Re-run the search so the newly filed events leave the list.
      generation.current++
      const { events: remaining } = await arcCandidates(arcId, {
        q: query.trim() || undefined,
        from: from || undefined,
        to: to || undefined,
      })
      setCandidates(remaining)
    } catch (err) {
      setError(message(err))
    } finally {
      setAttaching(false)
    }
  }

  return (
    <div className="finder">
      <label className="field">
        <span className="field__label">Search your events</span>
        <input
          className="field__input"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="auth, migration, oncall…"
          autoFocus
        />
      </label>

      <div className="field-row">
        <label className="field">
          <span className="field__label">
            From <span className="field__optional">optional</span>
          </span>
          <input
            className="field__input"
            type="date"
            value={from}
            onChange={(e) => setFrom(e.target.value)}
          />
        </label>
        <label className="field">
          <span className="field__label">
            To <span className="field__optional">optional</span>
          </span>
          <input
            className="field__input"
            type="date"
            value={to}
            onChange={(e) => setTo(e.target.value)}
          />
        </label>
      </div>

      <div className="finder__bar">
        <button
          className="link-button"
          onClick={toggleAll}
          disabled={visibleIds.length === 0}
        >
          {allPicked ? 'Clear all' : `Select all ${visibleIds.length}`}
        </button>
        <span className="counter">
          {searching ? 'searching…' : `${candidates.length} unfiled`}
        </span>
      </div>

      {error && <p className="alert">{error}</p>}
      {attached !== null && !error && (
        <p className="panel__hint">Filed {attached} {attached === 1 ? 'event' : 'events'}.</p>
      )}

      {!searching && candidates.length === 0 && (
        <p className="panel__hint">
          Nothing matches. Widen the search, or record it by hand on the timeline — the
          work that never reaches an API is usually the work worth claiming.
        </p>
      )}

      <ul className="candidates">
        {candidates.map((event) => (
          <li key={event.id} className="candidate">
            <label className="candidate__label">
              <input
                type="checkbox"
                checked={picked.has(event.id)}
                onChange={() => toggle(event.id)}
              />
              <span className="candidate__body">
                <span className="candidate__title">{event.title}</span>
                <span className="candidate__meta">
                  {dayLabel(event.occurred_at)} · {event.kind} · {event.source_label}
                  {event.other_arcs && event.other_arcs.length > 0 && (
                    <> · already in {event.other_arcs.join(', ')}</>
                  )}
                </span>
              </span>
            </label>
          </li>
        ))}
      </ul>

      <button
        className="button"
        onClick={() => void attach()}
        disabled={attaching || picked.size === 0}
      >
        {attaching ? 'Filing…' : `File ${picked.size} ${picked.size === 1 ? 'event' : 'events'}`}
      </button>
    </div>
  )
}
