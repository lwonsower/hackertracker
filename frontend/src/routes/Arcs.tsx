import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'

import { createArc, isEmpty, listArcs, type ArcRow } from '../api'

function relative(iso?: string): string {
  if (!iso) return 'nothing filed yet'
  const days = Math.round((Date.now() - new Date(iso).getTime()) / 86_400_000)
  if (days <= 0) return 'latest evidence today'
  if (days === 1) return 'latest evidence yesterday'
  if (days < 30) return `latest evidence ${days} days ago`
  if (days < 365) return `latest evidence ${Math.round(days / 30)} months ago`
  return `latest evidence ${Math.round(days / 365)} years ago`
}

/** Depth-first order, so a parent is immediately followed by what it holds. */
function nest(arcs: ArcRow[]): { arc: ArcRow; depth: number }[] {
  const byParent = new Map<string, ArcRow[]>()
  const known = new Set(arcs.map((a) => a.id))
  for (const arc of arcs) {
    // An arc whose parent is not in the list is shown at the top rather than
    // hidden: never drop a row because its parent is missing.
    const key = arc.parent_id && known.has(arc.parent_id) ? arc.parent_id : ''
    const bucket = byParent.get(key)
    if (bucket) bucket.push(arc)
    else byParent.set(key, [arc])
  }

  const out: { arc: ArcRow; depth: number }[] = []
  const walk = (key: string, depth: number) => {
    for (const arc of byParent.get(key) ?? []) {
      out.push({ arc, depth })
      walk(arc.id, depth + 1)
    }
  }
  walk('', 0)
  return out
}

export default function Arcs() {
  const [arcs, setArcs] = useState<ArcRow[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)

  const [title, setTitle] = useState('')
  const [parentID, setParentID] = useState('')
  const [startedAt, setStartedAt] = useState('')
  const [targetAt, setTargetAt] = useState('')
  const [summary, setSummary] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    try {
      const { arcs } = await listArcs()
      setArcs(arcs)
      setLoadError(null)
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : String(err))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void refresh()
  }, [refresh])

  const ordered = useMemo(() => nest(arcs), [arcs])
  const gaps = useMemo(() => arcs.filter(isEmpty), [arcs])

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setSaving(true)
    setSaveError(null)
    try {
      await createArc({
        title,
        status: 'open',
        parent_id: parentID || undefined,
        summary: summary.trim() || undefined,
        started_at: startedAt || undefined,
        target_at: targetAt || undefined,
      })
      setTitle('')
      setSummary('')
      setStartedAt('')
      setTargetAt('')
      await refresh()
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <>
      <h1 className="page__title">Arcs</h1>

      <div className="layout">
        <section className="panel" aria-labelledby="new-arc-heading">
          <h2 id="new-arc-heading" className="panel__title">
            New arc
          </h2>
          <p className="panel__hint">
            A line of work, or something you are aiming at. An arc can sit inside another,
            so <em>Promotion to Staff</em> holds <em>Cross-team influence</em>, which holds
            the work itself.
          </p>

          <form className="form" onSubmit={handleSubmit}>
            <label className="field">
              <span className="field__label">Title</span>
              <input
                className="field__input"
                value={title}
                onChange={(e) => setTitle(e.target.value)}
                placeholder="Auth service migration"
                required
              />
            </label>

            <label className="field">
              <span className="field__label">
                Inside <span className="field__optional">optional</span>
              </span>
              <select
                className="field__input"
                value={parentID}
                onChange={(e) => setParentID(e.target.value)}
              >
                <option value="">nothing — top level</option>
                {ordered.map(({ arc, depth }) => (
                  <option key={arc.id} value={arc.id}>
                    {'— '.repeat(depth)}
                    {arc.title}
                  </option>
                ))}
              </select>
            </label>

            <div className="field-row">
              <label className="field">
                <span className="field__label">
                  Started <span className="field__optional">optional</span>
                </span>
                <input
                  className="field__input"
                  type="date"
                  value={startedAt}
                  onChange={(e) => setStartedAt(e.target.value)}
                />
              </label>

              <label className="field">
                <span className="field__label">
                  Target <span className="field__optional">optional</span>
                </span>
                <input
                  className="field__input"
                  type="date"
                  value={targetAt}
                  onChange={(e) => setTargetAt(e.target.value)}
                />
              </label>
            </div>

            <label className="field">
              <span className="field__label">
                What it is <span className="field__optional">optional</span>
              </span>
              <textarea
                className="field__input field__input--area"
                value={summary}
                onChange={(e) => setSummary(e.target.value)}
                rows={3}
                placeholder="One line you would recognise this by in two years."
              />
            </label>

            {saveError && <p className="alert">{saveError}</p>}

            <button className="button" type="submit" disabled={saving || !title.trim()}>
              {saving ? 'Saving…' : 'Start it'}
            </button>
          </form>
        </section>

        <div className="arcs-column">
          {gaps.length > 0 && (
            <section className="panel panel--gap" aria-labelledby="gaps-heading">
              <h2 id="gaps-heading" className="panel__title">
                Nothing supports these yet
                <span className="counter">{gaps.length}</span>
              </h2>
              <p className="panel__hint">
                Named, and holding no work. Seen in March this is a to-do list; seen in
                December it is what you could not claim.
              </p>
              <ul className="gaps">
                {gaps.map((arc) => (
                  <li key={arc.id}>
                    <Link className="arc-card__link" to={`/arcs/${arc.id}`}>
                      {arc.title}
                    </Link>
                  </li>
                ))}
              </ul>
            </section>
          )}

          <section className="panel" aria-labelledby="arcs-heading">
            <h2 id="arcs-heading" className="panel__title">
              Arcs
              {arcs.length > 0 && <span className="counter">{arcs.length}</span>}
            </h2>

            {loading && <p className="panel__hint">Loading…</p>}
            {loadError && <p className="alert">{loadError}</p>}

            {!loading && !loadError && arcs.length === 0 && (
              <p className="panel__hint">
                No arcs yet. Events on their own do not survive a review — the arc is the
                thing you can actually claim.
              </p>
            )}

            <ul className="arcs">
              {ordered.map(({ arc, depth }) => (
                <li
                  key={arc.id}
                  className={isEmpty(arc) ? 'arc-card arc-card--empty' : 'arc-card'}
                  style={{ marginLeft: `calc(${depth} * var(--space-6))` }}
                >
                  <Link className="arc-card__link" to={`/arcs/${arc.id}`}>
                    <span className="arc-card__title">{arc.title}</span>
                  </Link>
                  <div className="arc-card__meta">
                    <span className={`badge badge--${arc.status}`}>{arc.status}</span>
                    {arc.child_count > 0 && (
                      <span className="arc-card__stat">
                        {arc.child_count} {arc.child_count === 1 ? 'sub-arc' : 'sub-arcs'}
                      </span>
                    )}
                    <span className="arc-card__stat">
                      {arc.event_count} {arc.event_count === 1 ? 'event' : 'events'}
                    </span>
                    <span className="arc-card__stat">
                      {arc.entry_count} {arc.entry_count === 1 ? 'entry' : 'entries'}
                    </span>
                    <span className="arc-card__stat">{relative(arc.last_event_at)}</span>
                  </div>
                  {arc.summary && <p className="arc-card__summary">{arc.summary}</p>}
                </li>
              ))}
            </ul>
          </section>
        </div>
      </div>
    </>
  )
}
