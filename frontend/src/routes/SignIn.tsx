import { useCallback, useEffect, useState } from 'react'
import { Navigate, useNavigate, useSearchParams } from 'react-router-dom'

import { authProviders, devSignIn, me, type Providers } from '../api'

/**
 * Google's own asset. Their branding guidelines require the standard-colour
 * mark, unaltered — so it is referenced rather than redrawn. To avoid the
 * third-party request, download the official SVG from
 * https://developers.google.com/identity/branding-guidelines into
 * frontend/public/ and point this at "/google-logo.svg".
 */
const GOOGLE_MARK = 'https://developers.google.com/identity/images/g-logo.png'

type Session = 'checking' | 'out' | 'in'

export default function SignIn() {
  const [session, setSession] = useState<Session>('checking')
  const [providers, setProviders] = useState<Providers | null>(null)
  const [markFailed, setMarkFailed] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [params] = useSearchParams()
  const navigate = useNavigate()

  // The server redirects here with ?error=… when a callback fails, so the
  // reason is readable instead of stranded in a log.
  const callbackError = params.get('error')

  useEffect(() => {
    // Someone who is already signed in has no business on this page.
    me()
      .then(() => setSession('in'))
      .catch(() => setSession('out'))
    authProviders()
      .then(setProviders)
      .catch(() => setProviders({ google: false, dev: false, self_host: false }))
  }, [])

  const handleDevSignIn = useCallback(async () => {
    setError(null)
    try {
      await devSignIn()
      navigate('/')
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [navigate])

  if (session === 'checking') return null
  if (session === 'in') return <Navigate to="/" replace />

  return (
    <main className="signin">
      <div className="signin__card">
        <div className="signin__intro">
          <h1 className="wordmark">hacker tracker</h1>
          <p className="signin__tagline">A record of what you actually did.</p>
        </div>

        {(callbackError || error) && <p className="alert">{callbackError ?? error}</p>}

        {providers === null && <p className="panel__hint">Loading…</p>}

        {providers?.google && (
          <a className="gsi" href="/api/auth/google/start?redirect_to=/">
            {!markFailed && (
              <img
                className="gsi__mark"
                src={GOOGLE_MARK}
                alt=""
                width={18}
                height={18}
                onError={() => setMarkFailed(true)}
              />
            )}
            <span className="gsi__label">Continue with Google</span>
          </a>
        )}

        {providers && !providers.google && (
          <p className="panel__hint">
            Google sign-in is not configured. Set <code>GOOGLE_CLIENT_ID</code>,{' '}
            <code>GOOGLE_CLIENT_SECRET</code> and <code>OAUTH_REDIRECT_URL</code> in{' '}
            <code>.env.local</code>, then restart the server.
          </p>
        )}

        {providers?.dev && (
          <div className="signin__dev">
            <button className="button button--quiet" onClick={() => void handleDevSignIn()}>
              Sign in as the local developer
            </button>
            <p className="field__help">
              Local development only. Bypasses Google entirely, and is refused unless the
              server is self-hosted with <code>DEV_SIGN_IN_EMAIL</code> set.
            </p>
          </div>
        )}
      </div>
    </main>
  )
}
