import { useCallback, useEffect, useState } from 'react'

import {
  authProviders,
  connectGitHub,
  listSources,
  syncSource,
  type SourceAccount,
  type SyncReport,
} from '../api'

function relative(iso?: string): string {
  if (!iso) return 'never synced'
  const seconds = Math.round((Date.now() - new Date(iso).getTime()) / 1000)
  if (seconds < 90) return 'just now'
  const minutes = Math.round(seconds / 60)
  if (minutes < 90) return `${minutes}m ago`
  const hours = Math.round(minutes / 60)
  if (hours < 36) return `${hours}h ago`
  return `${Math.round(hours / 24)}d ago`
}

/**
 * Catches a token pasted where a variable *name* belongs, before it leaves the
 * browser. The server rejects these too, but by then the value has travelled
 * through a request and back out in an error message — so the useful place to
 * stop it is here.
 */
const CREDENTIAL_PREFIXES = /^(github_pat_|ghp_|gho_|ghu_|ghs_|ghr_|glpat-|xox|sk-|sk_|rk_|AKIA|ASIA)/
const MAX_NAME_LENGTH = 64

function looksLikeCredential(value: string): boolean {
  return CREDENTIAL_PREFIXES.test(value) || value.length > MAX_NAME_LENGTH
}

/**
 * A one-line summary of what a sync examined, not just what it produced —
 * including the date it searched back to, which is the first thing you want
 * when a sync returns nothing.
 */
function summarise(report: SyncReport): string {
  const parts = [`${report.created} new`, `${report.updated} updated`]
  const queries = report.examined?.queries_issued
  if (queries) parts.push(`${queries} queries`)
  if (report.since) parts.push(`back to ${report.since.slice(0, 10)}`)
  if (!report.complete) parts.push('more remaining')
  return parts.join(' · ')
}

export default function Sources() {
  const [sources, setSources] = useState<SourceAccount[]>([])
  const [label, setLabel] = useState('GitHub')
  const [token, setToken] = useState('')
  const [envVar, setEnvVar] = useState('GITHUB_TOKEN')
  const [useEnvVar, setUseEnvVar] = useState(false)
  const [selfHost, setSelfHost] = useState(false)
  const [connecting, setConnecting] = useState(false)
  const [connectError, setConnectError] = useState<string | null>(null)
  const [syncing, setSyncing] = useState<string | null>(null)
  const [since, setSince] = useState('')
  const [reports, setReports] = useState<Record<string, SyncReport>>({})
  const [syncErrors, setSyncErrors] = useState<Record<string, string>>({})

  const refresh = useCallback(async () => {
    try {
      const { source_accounts } = await listSources()
      setSources(source_accounts)
    } catch {
      // The Sources panel is secondary; a load failure here shouldn't take
      // over the page when the timeline is still usable.
    }
  }, [])

  useEffect(() => {
    void refresh()
    authProviders()
      .then((p) => setSelfHost(p.self_host))
      .catch(() => setSelfHost(false))
  }, [refresh])

  async function handleConnect(e: React.FormEvent) {
    e.preventDefault()

    if (useEnvVar) {
      const name = envVar.trim()
      if (looksLikeCredential(name)) {
        setEnvVar('')
        setConnectError(
          'That looks like a token, not a variable name. Nothing was sent. ' +
            'Untick the environment-variable option to store the token instead, ' +
            'and revoke that token if it was real.',
        )
        return
      }
      if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(name)) {
        setConnectError('Use letters, digits and underscores, e.g. GITHUB_TOKEN.')
        return
      }
    } else if (!token.trim()) {
      setConnectError('Paste a personal access token.')
      return
    }

    setConnecting(true)
    setConnectError(null)
    try {
      const { login } = await connectGitHub(
        label.trim(),
        useEnvVar ? { credentialsRef: `env:${envVar.trim()}` } : { token: token.trim() },
      )
      setToken('')
      setLabel(`GitHub (${login})`)
      await refresh()
    } catch (err) {
      setConnectError(err instanceof Error ? err.message : String(err))
    } finally {
      setConnecting(false)
    }
  }

  async function handleSync(id: string) {
    setSyncing(id)
    setSyncErrors((prev) => ({ ...prev, [id]: '' }))
    try {
      const report = await syncSource(id, since || undefined)
      setReports((prev) => ({ ...prev, [id]: report }))
      await refresh()
    } catch (err) {
      setSyncErrors((prev) => ({ ...prev, [id]: err instanceof Error ? err.message : String(err) }))
      await refresh()
    } finally {
      setSyncing(null)
    }
  }

  return (
    <>
      <h1 className="page__title">Sources</h1>
      <section className="panel">
        <p className="panel__hint">
          Paste a personal access token. It is encrypted before it is stored, and
          never shown again.
        </p>

        <form className="form" onSubmit={handleConnect}>
          <label className="field">
            <span className="field__label">Label</span>
            <input
              className="field__input"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              required
            />
          </label>

          {useEnvVar ? (
            <label className="field">
              <span className="field__label">Variable name</span>
              <div className="field__prefixed">
                <span className="field__prefix">env:</span>
                <input
                  className="field__input"
                  value={envVar}
                  onChange={(e) => setEnvVar(e.target.value)}
                  placeholder="GITHUB_TOKEN"
                  autoComplete="off"
                  spellCheck={false}
                />
              </div>
            </label>
          ) : (
            <label className="field">
              <span className="field__label">Personal access token</span>
              <input
                className="field__input"
                type="password"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder="github_pat_…"
                autoComplete="off"
                spellCheck={false}
              />
            </label>
          )}

          {selfHost && (
            <label className="field__help field__toggle">
              <input
                type="checkbox"
                checked={useEnvVar}
                onChange={(e) => setUseEnvVar(e.target.checked)}
              />
              Use an environment variable instead (self-hosted only)
            </label>
          )}

          {connectError && <p className="alert">{connectError}</p>}

          <button className="button button--quiet" type="submit" disabled={connecting}>
            {connecting ? 'Verifying…' : 'Connect GitHub'}
          </button>
        </form>

        <label className="field sources__since">
          <span className="field__label">
            Backfill from <span className="field__optional">optional</span>
          </span>
          <input
            className="field__input"
            type="date"
            value={since}
            onChange={(e) => setSince(e.target.value)}
          />
          <span className="field__help">
            Leave empty to continue from the last successful sync, or ten years back on a
            first run. Set a date to reach further back.
          </span>
        </label>

        <ul className="sources">
          {sources.map((source) => {
            const report = reports[source.id]
            const failure = syncErrors[source.id] || source.last_error
            return (
              <li key={source.id} className="source">
                <div className="source__head">
                  <span className="source__label">{source.label}</span>
                  <span className="badge">{source.mode}</span>
                </div>
                <div className="source__meta">
                  {source.mode === 'pull' ? relative(source.last_synced_at) : 'receives pushes'}
                  {source.external_account_id && ` · ${source.external_account_id}`}
                </div>

                {source.mode === 'pull' && (
                  <button
                    className="button button--quiet"
                    onClick={() => void handleSync(source.id)}
                    disabled={syncing === source.id}
                  >
                    {syncing === source.id ? 'Syncing…' : 'Sync now'}
                  </button>
                )}

                {report && <p className="source__report">{summarise(report)}</p>}
                {report?.notes?.map((note) => (
                  <p key={note} className="alert">
                    {note}
                  </p>
                ))}
                {failure && <p className="alert">{failure}</p>}
              </li>
            )
          })}
        </ul>
      </section>
    </>
  )
}
