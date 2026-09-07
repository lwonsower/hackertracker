import { useEffect, useState } from 'react'
import { Navigate, NavLink, Outlet, useNavigate } from 'react-router-dom'

import { me, signOut, type Me } from '../api'

const NAV = [
  { to: '/', label: 'Timeline', end: true },
  { to: '/sources', label: 'Sources', end: false },
  { to: '/goals', label: 'Goals', end: false },
]

type State = { status: 'loading' } | { status: 'out' } | { status: 'in'; user: Me }

export default function AppShell() {
  const [state, setState] = useState<State>({ status: 'loading' })
  const navigate = useNavigate()

  // One check at the shell, so no page has to think about auth. Any failure
  // lands on the sign-in page: a 401 because you are signed out, and anything
  // else because a shell that cannot confirm who you are should not render
  // someone's private work history on the assumption that it is theirs.
  useEffect(() => {
    me()
      .then(({ user }) => setState({ status: 'in', user }))
      .catch(() => setState({ status: 'out' }))
  }, [])

  if (state.status === 'loading') return null
  if (state.status === 'out') return <Navigate to="/signin" replace />

  async function handleSignOut() {
    await signOut()
    navigate('/signin', { replace: true })
  }

  return (
    <div className="app">
      <header className="sidebar">
        <NavLink to="/" className="wordmark">
          hacker tracker
        </NavLink>

        <nav className="nav" aria-label="Main">
          {NAV.map(({ to, label, end }) => (
            <NavLink
              key={to}
              to={to}
              end={end}
              className={({ isActive }) => (isActive ? 'nav__link nav__link--active' : 'nav__link')}
            >
              {label}
            </NavLink>
          ))}
        </nav>

        <div className="sidebar__user">
          <span className="sidebar__email" title={state.user.email}>
            {state.user.email}
          </span>
          <button className="nav__link sidebar__signout" onClick={() => void handleSignOut()}>
            Sign out
          </button>
        </div>
      </header>

      <main className="content">
        <Outlet />
      </main>
    </div>
  )
}
