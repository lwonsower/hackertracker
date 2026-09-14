import { ChakraProvider, createSystem, defaultConfig, defineConfig } from '@chakra-ui/react'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter, Route, Routes } from 'react-router-dom'

import AppShell from './layout/AppShell'
import ArcPage from './routes/Arc'
import Arcs from './routes/Arcs'
import SignIn from './routes/SignIn'
import Sources from './routes/Sources'
import Timeline from './routes/Timeline'

import './styles/tokens.css'
import './styles/global.css'

// Chakra's stock theme, with its neutral accent swapped for the purple the app
// already uses. One line; delete it to get the default gray back.
const system = createSystem(
  defaultConfig,
  defineConfig({ globalCss: { html: { colorPalette: 'purple' } } }),
)

const rootElement = document.getElementById('root')
if (!rootElement) {
  throw new Error('#root not found — check index.html')
}

createRoot(rootElement).render(
  <StrictMode>
    {/* The `dark` class on <html> is what selects the dark palette. */}
    <ChakraProvider value={system}>
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
          </Route>
        </Routes>
      </BrowserRouter>
    </ChakraProvider>
  </StrictMode>,
)
