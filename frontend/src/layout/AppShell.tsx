import { Box, Button, Flex, Grid, Link as ChakraLink, Stack, Text } from '@chakra-ui/react'
import { useEffect, useState } from 'react'
import { Navigate, NavLink, Outlet, useNavigate } from 'react-router-dom'

import { me, signOut, type Me } from '../api'
import CaptureDialog from '../components/CaptureDialog'

const NAV = [
  { to: '/', label: 'Timeline', end: true },
  { to: '/arcs', label: 'Arcs', end: false },
  { to: '/sources', label: 'Sources', end: false },
]

type State = { status: 'loading' } | { status: 'out' } | { status: 'in'; user: Me }

/** What pages read from the router outlet. */
export type ShellContext = {
  /** Bumped on every successful capture, so a page can re-fetch on change. */
  captures: number
}

export default function AppShell() {
  const [state, setState] = useState<State>({ status: 'loading' })
  const [capturing, setCapturing] = useState(false)
  const [captures, setCaptures] = useState(0)
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
    <Grid
      minH="100vh"
      templateColumns={{ base: 'minmax(0, 1fr)', md: '13rem minmax(0, 1fr)' }}
    >
      {/* A bar across the top on a narrow screen, a column beside the page on a
          wide one. */}
      <Box
        as="header"
        display="flex"
        flexWrap="wrap"
        alignItems="center"
        gap="4"
        px="6"
        py="4"
        borderBottomWidth="1px"
        md={{
          display: 'block',
          position: 'sticky',
          top: 0,
          alignSelf: 'start',
          height: '100vh',
          py: '8',
          borderBottomWidth: 0,
          borderRightWidth: '1px',
        }}
      >
        <ChakraLink
          asChild
          display="block"
          fontSize="lg"
          letterSpacing="0.08em"
          color="fg"
          textDecoration="none"
          _hover={{ textDecoration: 'none' }}
        >
          <NavLink to="/">hacker tracker</NavLink>
        </ChakraLink>

        {/* Capture lives in the shell rather than on the timeline, because the
            thing worth recording occurs to you on whatever page you are on. */}
        <Button
          size="sm"
          onClick={() => setCapturing(true)}
          md={{ width: 'full', marginTop: '6' }}
        >
          Record something
        </Button>

        <Stack
          as="nav"
          aria-label="Main"
          direction="row"
          gap="1"
          md={{ display: 'grid', marginTop: '8' }}
        >
          {NAV.map(({ to, label, end }) => (
            <ChakraLink
              asChild
              key={to}
              px="3"
              py="2"
              fontSize="sm"
              borderRadius="l2"
              color="fg.muted"
              textDecoration="none"
              _hover={{ color: 'fg', bg: 'bg.subtle', textDecoration: 'none' }}
              // NavLink marks the active route with aria-current, so the state
              // is read off the link rather than tracked separately.
              _currentPage={{ color: 'fg', bg: 'bg.muted' }}
            >
              <NavLink to={to} end={end}>
                {label}
              </NavLink>
            </ChakraLink>
          ))}
        </Stack>

        <Flex
          alignItems="center"
          gap="2"
          ms="auto"
          minW="0"
          md={{ display: 'grid', justifyItems: 'start', gap: '1', margin: '2rem 0 0' }}
        >
          <Text
            fontSize="xs"
            color="fg.subtle"
            maxW="100%"
            overflow="hidden"
            textOverflow="ellipsis"
            whiteSpace="nowrap"
            title={state.user.email}
          >
            {state.user.email}
          </Text>
          <Button
            variant="plain"
            size="sm"
            px="0"
            height="auto"
            justifyContent="start"
            color="fg.muted"
            _hover={{ color: 'fg' }}
            onClick={() => void handleSignOut()}
          >
            Sign out
          </Button>
        </Flex>
      </Box>

      <Box as="main" minW="0" px="6" pt="8" pb="12">
        <Outlet context={{ captures } satisfies ShellContext} />
      </Box>

      <CaptureDialog
        open={capturing}
        onClose={() => setCapturing(false)}
        onSaved={() => setCaptures((n) => n + 1)}
      />
    </Grid>
  )
}
