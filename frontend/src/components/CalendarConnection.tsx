import { useCallback, useEffect, useState } from 'react'

import { calendarStatus, disconnectCalendar, type CalendarStatus } from '../api'

function reviewed(iso?: string): string {
  if (!iso) return 'never reviewed'
  const days = Math.round((Date.now() - new Date(iso).getTime()) / 86_400_000)
  if (days <= 0) return 'reviewed today'
  if (days === 1) return 'reviewed yesterday'
  if (days < 30) return `reviewed ${days} days ago`
  return `reviewed ${Math.round(days / 30)} months ago`
}

/**
 * The calendar's home on the Sources page. The review banner is where you use
 * the connection; this is where you can see it exists and take it away.
 */
export default function CalendarConnection() {
  const [status, setStatus] = useState<CalendarStatus | null>(null)
  const [confirming, setConfirming] = useState(false)
  const [working, setWorking] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      setStatus(await calendarStatus())
    } catch {
      setStatus(null)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  if (!status?.available) return null

  async function handleDisconnect() {
    setWorking(true)
    setError(null)
    try {
      await disconnectCalendar()
      setConfirming(false)
      await load()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setWorking(false)
    }
  }

  return (
    <section className="panel" aria-labelledby="calendar-heading">
      <h2 id="calendar-heading" className="panel__title">
        Google Calendar
        {status.connected && <span className="badge">review</span>}
      </h2>

      {!status.connected ? (
        <>
          <p className="panel__hint">
            {status.disconnected
              ? 'Disconnected. The meetings you recorded are still on your timeline.'
              : 'Meetings are never synced. Each week you are shown what happened and you choose what to keep.'}
          </p>
          <a className="button button--quiet sources__connect" href="/api/calendar/connect">
            {status.disconnected ? 'Reconnect' : 'Connect'}
          </a>
        </>
      ) : (
        <>
          <div className="source__meta sources__calendar-meta">
            {status.account}
            {` · ${reviewed(status.last_reviewed_at)}`}
          </div>
          <p className="panel__hint">
            Read-only. Nothing from your calendar is stored unless you pick it during a
            review.
          </p>

          <div className="arc-head__actions sources__calendar-actions">
            <a className="button button--quiet" href="/?calendar=review">
              Review meetings
            </a>
          </div>

          {confirming ? (
            <div className="sources__confirm">
              <p className="alert">
                This removes our access to your calendar. Meetings you already recorded
                stay on your timeline, with the notes you wrote.
              </p>
              <div className="arc-head__actions">
                <button
                  className="button button--quiet"
                  onClick={() => void handleDisconnect()}
                  disabled={working}
                >
                  {working ? 'Disconnecting…' : 'Yes, disconnect'}
                </button>
                <button className="link-button" onClick={() => setConfirming(false)}>
                  Cancel
                </button>
              </div>
            </div>
          ) : (
            <button className="button button--quiet" onClick={() => setConfirming(true)}>
              Disconnect
            </button>
          )}

          {error && <p className="alert">{error}</p>}
        </>
      )}
    </section>
  )
}
