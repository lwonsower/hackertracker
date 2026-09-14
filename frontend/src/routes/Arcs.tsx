import {
  Alert,
  Badge,
  Box,
  Button,
  Card,
  Field,
  Grid,
  Heading,
  HStack,
  Input,
  Link as ChakraLink,
  Spinner,
  Stack,
  Text,
  Textarea,
} from '@chakra-ui/react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'

import { createArc, isEmpty, listArcs, type ArcLink, type ArcRow } from '../api'
import DateField from '../components/DateField'

function relative(iso?: string): string {
  if (!iso) return 'nothing filed yet'
  const days = Math.round((Date.now() - new Date(iso).getTime()) / 86_400_000)
  if (days <= 0) return 'latest evidence today'
  if (days === 1) return 'latest evidence yesterday'
  if (days < 30) return `latest evidence ${days} days ago`
  if (days < 365) return `latest evidence ${Math.round(days / 30)} months ago`
  return `latest evidence ${Math.round(days / 365)} years ago`
}

type Placed = { arc: ArcRow; depth: number; repeat: boolean; key: string }

/**
 * Lays the evidence graph out depth-first.
 *
 * An arc can be filed into several others, so it legitimately appears more
 * than once — that is the fact, not a bug. Later appearances are marked so the
 * reader knows it is the same arc, and are not expanded again, which also
 * makes the walk terminate whatever the server allowed.
 */
function layout(arcs: ArcRow[], links: ArcLink[]): Placed[] {
  const byID = new Map(arcs.map((a) => [a.id, a]))
  const heldBy = new Map<string, string[]>()
  const filed = new Set<string>()
  for (const link of links) {
    if (!byID.has(link.arc_id) || !byID.has(link.evidence_id)) continue
    filed.add(link.evidence_id)
    const bucket = heldBy.get(link.arc_id)
    if (bucket) bucket.push(link.evidence_id)
    else heldBy.set(link.arc_id, [link.evidence_id])
  }

  const out: Placed[] = []
  const seen = new Set<string>()

  const walk = (id: string, depth: number, path: Set<string>) => {
    const arc = byID.get(id)
    if (!arc) return
    const repeat = seen.has(id)
    seen.add(id)
    out.push({ arc, depth, repeat, key: `${[...path, id].join('>')}` })
    // Stop at a repeat, and never re-enter something already on this path.
    if (repeat || path.has(id)) return
    const next = new Set(path)
    next.add(id)
    for (const child of heldBy.get(id) ?? []) walk(child, depth + 1, next)
  }

  // Roots are the arcs nothing holds. Anything left over is only reachable
  // through a loop, and is shown at the top rather than dropped.
  for (const arc of arcs) if (!filed.has(arc.id)) walk(arc.id, 0, new Set())
  for (const arc of arcs) if (!seen.has(arc.id)) walk(arc.id, 0, new Set())
  return out
}

const STATUS_PALETTE: Record<string, string> = {
  open: 'purple',
  done: 'gray',
  dropped: 'gray',
}

export default function Arcs() {
  const [arcs, setArcs] = useState<ArcRow[]>([])
  const [links, setLinks] = useState<ArcLink[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)

  const [title, setTitle] = useState('')
  const [startedAt, setStartedAt] = useState('')
  const [targetAt, setTargetAt] = useState('')
  const [summary, setSummary] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    try {
      const { arcs, links } = await listArcs()
      setArcs(arcs)
      setLinks(links)
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

  const ordered = useMemo(() => layout(arcs, links), [arcs, links])
  const gaps = useMemo(() => arcs.filter(isEmpty), [arcs])

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setSaving(true)
    setSaveError(null)
    try {
      await createArc({
        title,
        status: 'open',
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
      <Heading as="h1" fontSize="xl" fontWeight="normal" mb="6">
        Arcs
      </Heading>

      <Grid
        templateColumns={{ base: 'minmax(0, 1fr)', lg: '22rem minmax(0, 1fr)' }}
        gap="6"
        alignItems="start"
      >
        <Card.Root
          as="section"
          aria-labelledby="new-arc-heading"
          position={{ lg: 'sticky' }}
          top="8"
        >
          <Card.Header>
            <Card.Title id="new-arc-heading" fontSize="lg" fontWeight="medium">
              New arc
            </Card.Title>
            <Card.Description>
              A line of work, or something you are aiming at. File arcs into each other as
              evidence from the arc's own page — one piece of work can support several
              things at once.
            </Card.Description>
          </Card.Header>

          <Card.Body as="form" onSubmit={handleSubmit}>
            <Stack gap="4">
              <Field.Root required>
                <Field.Label>Title</Field.Label>
                <Input
                  value={title}
                  onChange={(e) => setTitle(e.target.value)}
                  placeholder="Auth service migration"
                />
              </Field.Root>

              <Stack direction={{ base: 'column', sm: 'row' }} gap="4">
                <DateField
                  label={
                    <>
                      Started{' '}
                      <Text as="span" color="fg.subtle">
                        optional
                      </Text>
                    </>
                  }
                  value={startedAt}
                  onChange={setStartedAt}
                  flex="1"
                />
                <DateField
                  label={
                    <>
                      Target{' '}
                      <Text as="span" color="fg.subtle">
                        optional
                      </Text>
                    </>
                  }
                  value={targetAt}
                  onChange={setTargetAt}
                  flex="1"
                />
              </Stack>

              <Field.Root>
                <Field.Label>What it is</Field.Label>
                <Textarea
                  rows={3}
                  value={summary}
                  onChange={(e) => setSummary(e.target.value)}
                  placeholder="One line you would recognise this by in two years."
                />
                <Field.HelperText>Optional.</Field.HelperText>
              </Field.Root>

              {saveError && (
                <Alert.Root status="error">
                  <Alert.Indicator />
                  <Alert.Title>{saveError}</Alert.Title>
                </Alert.Root>
              )}

              <Button
                type="submit"
                alignSelf="start"
                loading={saving}
                disabled={!title.trim()}
              >
                Start it
              </Button>
            </Stack>
          </Card.Body>
        </Card.Root>

        <Stack gap="6" minW="0">
          {gaps.length > 0 && (
            <Card.Root
              as="section"
              aria-labelledby="gaps-heading"
              borderLeftWidth="2px"
              borderLeftColor="colorPalette.solid"
            >
              <Card.Body>
                <HStack id="gaps-heading" as="h2" gap="3" fontSize="lg" fontWeight="medium">
                  <Text>Nothing supports these yet</Text>
                  <Text fontSize="sm" color="fg.subtle" fontVariantNumeric="tabular-nums">
                    {gaps.length}
                  </Text>
                </HStack>

                <Stack as="ul" listStyleType="none" mt="4" gap="2" fontSize="sm">
                  {gaps.map((arc) => (
                    <Box as="li" key={arc.id}>
                      <ChakraLink asChild color="fg">
                        <Link to={`/arcs/${arc.id}`}>{arc.title}</Link>
                      </ChakraLink>
                    </Box>
                  ))}
                </Stack>
              </Card.Body>
            </Card.Root>
          )}

          <Card.Root as="section" aria-labelledby="arcs-heading">
            <Card.Body>
              <HStack id="arcs-heading" as="h2" gap="3" fontSize="lg" fontWeight="medium">
                <Text>Arcs</Text>
                {arcs.length > 0 && (
                  <Text fontSize="sm" color="fg.subtle" fontVariantNumeric="tabular-nums">
                    {arcs.length}
                  </Text>
                )}
              </HStack>

              {loading && (
                <HStack mt="3" gap="2" color="fg.muted" fontSize="sm">
                  <Spinner size="xs" />
                  <Text>Loading…</Text>
                </HStack>
              )}

              {loadError && (
                <Alert.Root status="error" mt="3">
                  <Alert.Indicator />
                  <Alert.Title>{loadError}</Alert.Title>
                </Alert.Root>
              )}

              {!loading && !loadError && arcs.length === 0 && (
                <Text mt="3" fontSize="sm" color="fg.muted">
                  No arcs yet. Events on their own do not survive a review — the arc is the
                  thing you can actually claim.
                </Text>
              )}

              <Stack as="ul" listStyleType="none" mt="6" gap="3">
                {ordered.map(({ arc, depth, repeat, key }) => (
                  <Box
                    as="li"
                    key={key}
                    ms={`calc(${depth} * var(--chakra-spacing-6))`}
                    p="3"
                    borderRadius="l2"
                    bg={repeat || isEmpty(arc) ? 'transparent' : 'bg.muted'}
                    borderWidth={repeat || isEmpty(arc) ? '1px' : undefined}
                    borderStyle={isEmpty(arc) && !repeat ? 'dashed' : 'solid'}
                    opacity={repeat ? 0.7 : undefined}
                  >
                    <ChakraLink asChild color="fg" textDecoration="none">
                      <Link to={`/arcs/${arc.id}`}>{arc.title}</Link>
                    </ChakraLink>

                    {repeat ? (
                      <Text mt="2" fontSize="xs" color="fg.subtle">
                        also filed under {arc.supports_count - 1} other
                        {arc.supports_count - 1 === 1 ? '' : 's'} — shown above
                      </Text>
                    ) : (
                      <HStack mt="2" wrap="wrap" gap="2">
                        <Badge
                          variant="outline"
                          fontFamily="mono"
                          fontSize="xs"
                          colorPalette={STATUS_PALETTE[arc.status] ?? 'gray'}
                        >
                          {arc.status}
                        </Badge>
                        {arc.arc_count > 0 && (
                          <Text fontSize="xs" color="fg.subtle">
                            {arc.arc_count} {arc.arc_count === 1 ? 'arc' : 'arcs'}
                          </Text>
                        )}
                        <Text fontSize="xs" color="fg.subtle">
                          {arc.event_count} {arc.event_count === 1 ? 'event' : 'events'}
                        </Text>
                        <Text fontSize="xs" color="fg.subtle">
                          {arc.entry_count} {arc.entry_count === 1 ? 'entry' : 'entries'}
                        </Text>
                        <Text fontSize="xs" color="fg.subtle">
                          {relative(arc.last_event_at)}
                        </Text>
                      </HStack>
                    )}

                    {!repeat && arc.summary && (
                      <Text mt="2" fontSize="sm" color="fg.muted" whiteSpace="pre-wrap">
                        {arc.summary}
                      </Text>
                    )}
                  </Box>
                ))}
              </Stack>
            </Card.Body>
          </Card.Root>
        </Stack>
      </Grid>
    </>
  )
}
