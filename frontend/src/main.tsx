import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter, Route, Routes } from 'react-router-dom'

import AppShell from './layout/AppShell'
import ArcPage from './routes/Arc'
import Arcs from './routes/Arcs'
import Goals from './routes/Goals'
import SignIn from './routes/SignIn'
import Sources from './routes/Sources'
import Timeline from './routes/Timeline'

import './styles/tokens.css'
import './styles/global.css'

const rootElement = document.getElementById('root')
if (!rootElement) {
  throw new Error('#root not found — check index.html')
}

createRoot(rootElement).render(
  <StrictMode>
    <BrowserRouter>
      <Routes>
        {/* Outside the shell: the shell requires a session, and this is where
            you land when you do not have one. */}
        <Route path="/signin" element={<SignIn />} />

        {/* The shell renders the sidebar; pages render into its outlet.
            Deep links survive a hard refresh because the Go server falls back
            to index.html for any path outside /api. */}
        <Route element={<AppShell />}>
          <Route path="/" element={<Timeline />} />
          <Route path="/arcs" element={<Arcs />} />
          <Route path="/arcs/:id" element={<ArcPage />} />
          <Route path="/sources" element={<Sources />} />
          <Route path="/goals" element={<Goals />} />
        </Route>
      </Routes>
    </BrowserRouter>
  </StrictMode>,
)
