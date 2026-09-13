import { useCallback, useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'

import {
  ApiError,
  calendarProposals,
  calendarStatus,
  promoteProposals,
  type CalendarStatus,
  type Proposal,
} from '../api'

/**
 * What the OAuth callback redirects back with. Without these a refused or
 * abandoned consent screen returns you to an unchanged page with no
 * explanation, which reads as the button being broken.
 */
const OUTCOMES: Record<string, string> = {
  connected: 'Calendar connected.',
  denied: 'Google access was not granted, so no calendar is connected.',
  expired: 'That connection attempt timed out. Try again.',
  mismatch: 'That connection was started from a different account. Try again.',
  failed: 'Google could not complete the connection. Try again.',
}

function dayLabel(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
  })
}

function timeLabel(iso: string): string {
  return new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
}

function lengthLabel(p: Proposal): string {
  if (!p.end || p.all_day) return ''
  const minutes = Math.round((new Date(p.end).getTime() - new Date(p.start).getTime()) / 60_000)
  if (minutes <= 0) return ''
  return minutes < 60 ? `${minutes}m` : `${Math.round((minutes / 60) * 10) / 10}h`
}

function people(p: Proposal): string {
  const names = p.attendees ?? []
  if (names.length === 0) return 'no one else'
  if (names.length <= 3) return names.join(', ')
  return `${names.slice(0, 2).join(', ')} +${names.length - 2}`
}

function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

function needsReconnect(err: unknown): boolean {
  return err instanceof ApiError && err.reconnect
}

/**
 * Sits above the timeline and appears only when meetings are waiting, so
 * reviewing is something you meet on the page you already open rather than a
 * page you have to remember to visit.
 */
export default function ReviewBanner({ onAdded }: { onAdded: () => void }) {
  const [status, setStatus] = useState<CalendarStatus | null>(null)
  const [open, setOpen] = useState(false)
  const [proposals, setProposals] = useState<Proposal[]>([])
  const [suggested, setSuggested] = useState(0)
  const [hidden, setHidden] = useState(0)
  const [showHidden, setShowHidden] = useState(false)
  const [days, setDays] = useState(0)
  const [picked, setPicked] = useState<Map<string, string>>(() => new Map())
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [reconnect, setReconnect] = useState(false)

  const [params, setParams] = useSearchParams()
  const [notice, setNotice] = useState<string | null>(null)

  useEffect(() => {
    calendarStatus()
      .then(setStatus)
      .catch(() => setStatus(null))
  }, [])

  // Read the outcome into state before dropping it from the URL. Keeping it
  // only in the URL loses it: this component renders nothing until the status
  // call returns, and by then the param would already be cleared.
  useEffect(() => {
    const outcome = params.get('calendar')
    if (!outcome) return
    setNotice(OUTCOMES[outcome] ?? null)
    // Connecting and then being shown nothing is the worst possible first
    // impression, so the review opens itself at that moment.
    if (outcome === 'connected' || outcome === 'review') setOpen(true)
    const next = new URLSearchParams(params)
    next.delete('calendar')
    setParams(next, { replace: true })
    // Once, on arrival: re-running as params change would re-announce it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      // days === 0 means "since I last reviewed", which the server works out.
      const from =
        days > 0 ? new Date(Date.now() - days * 86_400_000).toISOString() : undefined
      const result = await calendarProposals({ from })
      setProposals(result.proposals)
      setSuggested(result.suggested)
      setHidden(result.hidden)
    } catch (err) {
      setError(message(err))
      setReconnect(needsReconnect(err))
    } finally {
      setLoading(false)
    }
  }, [days])

  useEffect(() => {
    if (status?.connected) void load()
  }, [status?.connected, load])

  if (!status?.available) return null

  if (!status.connected) {
    return (
      <section className="review review--offer">
        {notice && <p className="alert">{notice}</p>}
        {status.disconnected ? (
          <p className="review__lead">
            Your calendar is disconnected. The meetings you already recorded are still on
            your timeline — reconnect to start reviewing new ones again.
          </p>
        ) : (
          <>
            <p className="review__lead">
              Most of what you do never reaches an API. Connect your calendar and
              HackerTracker will ask you each week which meetings were worth recording.
            </p>
            <p className="review__fine">
              Read-only, and nothing is stored unless you pick it — the meetings you skip
              are never written down.
            </p>
          </>
        )}
        <a className="button" href="/api/calendar/connect">
          {status.disconnected ? 'Reconnect Google Calendar' : 'Connect Google Calendar'}
        </a>
      </section>
    )
  }

  const waiting = suggested
  // A failure keeps the banner on screen. Hiding it would mean a calendar that
  // quietly stopped working looks identical to one with nothing to review —
  // and in Testing status a Google grant expires every seven days.
  // Before the first review there is always a way in, even with nothing
  // suggested — otherwise a freshly connected calendar shows no sign of itself.
  if (!open && waiting === 0 && !loading && !error && status.last_reviewed_at) return null

  const visible = proposals.filter((p) => !p.already_added && (showHidden || !p.excluded))

  function toggle(p: Proposal) {
    setPicked((current) => {
      const next = new Map(current)
      if (next.has(p.id)) next.delete(p.id)
      else next.set(p.id, '')
      return next
    })
  }

  function setNote(id: string, note: string) {
    setPicked((current) => new Map(current).set(id, note))
  }

  async function save() {
    setSaving(true)
    setError(null)
    try {
      const picks = [...picked].map(([id, note]) => ({ id, note }))
      await promoteProposals(picks, new Date().toISOString())
      setPicked(new Map())
      setOpen(false)
      onAdded()
      setStatus(await calendarStatus())
      await load()
    } catch (err) {
      setError(message(err))
      setReconnect(needsReconnect(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <section className="review">
      {notice && <p className="alert">{notice}</p>}

      <div className="review__head">
        <p className="review__lead">
          {reconnect
            ? 'Your calendar connection has stopped working.'
            : waiting > 0
              ? `${waiting} ${waiting === 1 ? 'meeting' : 'meetings'} since you last looked.`
              : 'Nothing new on your calendar.'}
        </p>
        {reconnect ? (
          <a className="button button--quiet sources__connect" href="/api/calendar/connect">
            Reconnect
          </a>
        ) : (
          <button className="button button--quiet" onClick={() => setOpen((v) => !v)}>
            {open ? 'Later' : 'Review them'}
          </button>
        )}
      </div>

      {/* The reconnect case is already stated in the heading, with a button
          beside it; repeating the raw message would just say it twice. */}
      {error && !reconnect && <p className="alert review__error">{error}</p>}

      {open && (
        <>
          <p className="review__fine">
            Tick what mattered and say what came of it. <em>Design review</em> is a calendar
            entry; <em>design review — got sign-off on the sharded approach</em> is evidence.
          </p>

          {loading && <p className="panel__hint">Reading your calendar…</p>}

          <div className="proposals-scroll">
            <ul className="proposals">
            {visible.map((p) => {
              const isPicked = picked.has(p.id)
              return (
                <li key={p.id} className={isPicked ? 'proposal proposal--picked' : 'proposal'}>
                  <label className="proposal__label">
                    <input type="checkbox" checked={isPicked} onChange={() => toggle(p)} />
                    <span className="proposal__body">
                      <span className="proposal__title">{p.title}</span>
                      <span className="proposal__meta">
                        {dayLabel(p.start)}
                        {!p.all_day && ` · ${timeLabel(p.start)}`}
                        {lengthLabel(p) && ` · ${lengthLabel(p)}`}
                        {` · ${people(p)}`}
                        {p.excluded && (
                          <span className="proposal__filter"> · {p.excluded}</span>
                        )}
                      </span>
                    </span>
                  </label>

                  {isPicked && (
                    <input
                      className="field__input proposal__note"
                      value={picked.get(p.id) ?? ''}
                      onChange={(e) => setNote(p.id, e.target.value)}
                      placeholder="What came of it?"
                      autoFocus
                    />
                  )}
                </li>
              )
            })}
            </ul>
          </div>

          {!loading && visible.length === 0 && (
            <p className="panel__hint">
              Nothing to review in {days > 0 ? `the last ${days} days` : 'this window'}.
              {hidden > 0 && ' Everything here was filtered out — standups, solo blocks and'}
              {hidden > 0 && ' the like. Show them below, or look further back.'}
            </p>
          )}

          <div className="finder__bar">
            <span className="review__fine">Looking at</span>
            <span className="review__range">
              {[
                { value: 0, label: 'since last review' },
                { value: 7, label: '7 days' },
                { value: 30, label: '30 days' },
                { value: 90, label: '90 days' },
              ].map((option) => (
                <button
                  key={option.value}
                  className={
                    days === option.value ? 'link-button link-button--on' : 'link-button'
                  }
                  onClick={() => setDays(option.value)}
                >
                  {option.label}
                </button>
              ))}
            </span>
          </div>

          <div className="review__actions">
            <button className="button" onClick={() => void save()} disabled={saving}>
              {saving
                ? 'Saving…'
                : picked.size > 0
                  ? `Record ${picked.size}`
                  : 'Nothing this week'}
            </button>
            {hidden > 0 && (
              <button className="link-button" onClick={() => setShowHidden((v) => !v)}>
                {showHidden ? 'Hide filtered' : `Show ${hidden} filtered out`}
              </button>
            )}
          </div>
        </>
      )}
    </section>
  )
}
