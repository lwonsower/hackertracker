import {
  Alert,
  Badge,
  Box,
  Button,
  Card,
  Checkbox,
  Field,
  Heading,
  HStack,
  Input,
  InputGroup,
  Stack,
  Text,
} from '@chakra-ui/react'
import { useCallback, useEffect, useState } from 'react'

import {
  authProviders,
  connectGitHub,
  listSources,
  syncSource,
  type SourceAccount,
  type SyncReport,
} from '../api'
import CalendarConnection from '../components/CalendarConnection'
import DateField from '../components/DateField'

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
const CREDENTIAL_PREFIXES =
  /^(github_pat_|ghp_|gho_|ghu_|ghs_|ghr_|glpat-|xox|sk-|sk_|rk_|AKIA|ASIA)/
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
      setSyncErrors((prev) => ({
        ...prev,
        [id]: err instanceof Error ? err.message : String(err),
      }))
      await refresh()
    } finally {
      setSyncing(null)
    }
  }

  const connectors = sources.filter((source) => source.source !== 'google_calendar')

  return (
    <>
      <Heading as="h1" fontSize="xl" fontWeight="normal" mb="6">
        Sources
      </Heading>

      <Stack gap="6">
        <Card.Root as="section">
          <Card.Body>
            <Text fontSize="sm" color="fg.muted">
              Paste a personal access token. It is encrypted before it is stored, and never
              shown again.
            </Text>

            <Stack as="form" onSubmit={handleConnect} gap="4" mt="6" maxW="lg">
              <Field.Root required>
                <Field.Label>Label</Field.Label>
                <Input value={label} onChange={(e) => setLabel(e.target.value)} />
              </Field.Root>

              {useEnvVar ? (
                <Field.Root>
                  <Field.Label>Variable name</Field.Label>
                  <InputGroup
                    startElement={
                      <Text fontFamily="mono" fontSize="sm" color="fg.subtle">
                        env:
                      </Text>
                    }
                  >
                    <Input
                      // Clears the `env:` prefix, which is wider than the
                      // padding an icon-sized start element gets.
                      ps="12"
                      value={envVar}
                      onChange={(e) => setEnvVar(e.target.value)}
                      placeholder="GITHUB_TOKEN"
                      autoComplete="off"
                      spellCheck={false}
                    />
                  </InputGroup>
                </Field.Root>
              ) : (
                <Field.Root>
                  <Field.Label>Personal access token</Field.Label>
                  <Input
                    type="password"
                    value={token}
                    onChange={(e) => setToken(e.target.value)}
                    placeholder="github_pat_…"
                    autoComplete="off"
                    spellCheck={false}
                  />
                </Field.Root>
              )}

              {selfHost && (
                <Checkbox.Root
                  checked={useEnvVar}
                  onCheckedChange={(details) => setUseEnvVar(details.checked === true)}
                  size="sm"
                >
                  <Checkbox.HiddenInput />
                  <Checkbox.Control />
                  <Checkbox.Label color="fg.subtle" fontSize="xs">
                    Use an environment variable instead (self-hosted only)
                  </Checkbox.Label>
                </Checkbox.Root>
              )}

              {connectError && (
                <Alert.Root status="error">
                  <Alert.Indicator />
                  <Alert.Title>{connectError}</Alert.Title>
                </Alert.Root>
              )}

              <Button
                type="submit"
                variant="outline"
                size="sm"
                alignSelf="start"
                loading={connecting}
                loadingText="Verifying…"
              >
                Connect GitHub
              </Button>
            </Stack>

            <Box mt="6" maxW="xs">
              <DateField
                label={
                  <>
                    Backfill from{' '}
                    <Text as="span" color="fg.subtle">
                      optional
                    </Text>
                  </>
                }
                value={since}
                onChange={setSince}
              />
              <Text mt="2" fontSize="xs" color="fg.subtle">
                Leave empty to continue from the last successful sync, or ten years back on
                a first run. Set a date to reach further back.
              </Text>
            </Box>

            <Stack as="ul" listStyleType="none" mt="6" gap="3">
              {connectors.map((source) => {
                const report = reports[source.id]
                const failure = syncErrors[source.id] || source.last_error
                return (
                  <Stack
                    as="li"
                    key={source.id}
                    gap="2"
                    alignItems="start"
                    p="3"
                    bg="bg.muted"
                    borderRadius="l2"
                  >
                    <HStack gap="2">
                      <Text fontSize="sm">{source.label}</Text>
                      <Badge variant="outline" fontFamily="mono" fontSize="xs">
                        {source.mode}
                      </Badge>
                    </HStack>

                    <Text fontSize="xs" color="fg.subtle">
                      {source.mode === 'pull'
                        ? relative(source.last_synced_at)
                        : 'receives pushes'}
                      {source.external_account_id && ` · ${source.external_account_id}`}
                    </Text>

                    {source.mode === 'pull' && (
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => void handleSync(source.id)}
                        loading={syncing === source.id}
                        loadingText="Syncing…"
                      >
                        Sync now
                      </Button>
                    )}

                    {report && (
                      <Text
                        fontSize="xs"
                        color="fg.muted"
                        fontVariantNumeric="tabular-nums"
                      >
                        {summarise(report)}
                      </Text>
                    )}

                    {report?.notes?.map((note) => (
                      <Alert.Root key={note} status="info" size="sm">
                        <Alert.Indicator />
                        <Alert.Title>{note}</Alert.Title>
                      </Alert.Root>
                    ))}

                    {failure && (
                      <Alert.Root status="error" size="sm">
                        <Alert.Indicator />
                        <Alert.Title>{failure}</Alert.Title>
                      </Alert.Root>
                    )}
                  </Stack>
                )
              })}
            </Stack>
          </Card.Body>
        </Card.Root>

        <CalendarConnection />
      </Stack>
    </>
  )
}
