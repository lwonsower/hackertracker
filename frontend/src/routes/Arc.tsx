import {
  Alert,
  Badge,
  Box,
  Button,
  Card,
  Checkbox,
  Field,
  Grid,
  Heading,
  HStack,
  Input,
  Link as ChakraLink,
  Spinner,
  Stack,
  Tabs,
  Text,
  Textarea,
} from '@chakra-ui/react'
import type { ReactNode } from 'react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'

import {
  addArcEntry,
  arcCandidateArcs,
  arcCandidates,
  attachArcs,
  attachEvents,
  detachArc,
  detachEvent,
  getArc,
  isEmpty,
  updateArc,
  ENTRY_KINDS,
  type Arc,
  type ArcEntry,
  type ArcEvent,
  type ArcRow,
} from '../api'
import DateField from '../components/DateField'
import SelectField from '../components/SelectField'

function dayLabel(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  })
}

/** Value for a date field, which wants the local calendar day. */
function toDateInputValue(date: Date): string {
  const offset = date.getTimezoneOffset() * 60_000
  return new Date(date.getTime() - offset).toISOString().slice(0, 10)
}

function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

const STATUS_PALETTE: Record<string, string> = {
  open: 'purple',
  done: 'gray',
  dropped: 'gray',
}

const STATUSES = [
  { label: 'open', value: 'open' },
  { label: 'done', value: 'done' },
  { label: 'dropped', value: 'dropped' },
]

const ENTRY_KIND_ITEMS = [
  { label: 'none', value: 'none' },
  ...ENTRY_KINDS.map((k) => ({ label: k, value: k })),
]

function ErrorText({ children }: { children: ReactNode }) {
  return (
    <Alert.Root status="error">
      <Alert.Indicator />
      <Alert.Title>{children}</Alert.Title>
    </Alert.Root>
  )
}

export default function ArcPage() {
  const { id = '' } = useParams()

  const [arc, setArc] = useState<Arc | null>(null)
  const [entries, setEntries] = useState<ArcEntry[]>([])
  const [events, setEvents] = useState<ArcEvent[]>([])
  const [evidenceArcs, setEvidenceArcs] = useState<ArcRow[]>([])
  const [supports, setSupports] = useState<Arc[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      const { arc, entries, events, evidence_arcs, supports } = await getArc(id)
      setArc(arc)
      setEntries(entries)
      setEvents(events)
      setEvidenceArcs(evidence_arcs)
      setSupports(supports)
      setLoadError(null)
    } catch (err) {
      setLoadError(message(err))
    } finally {
      setLoading(false)
    }
  }, [id])

  useEffect(() => {
    void load()
  }, [load])

  if (loading) {
    return (
      <HStack gap="2" color="fg.muted" fontSize="sm">
        <Spinner size="xs" />
        <Text>Loading…</Text>
      </HStack>
    )
  }
  if (loadError) return <ErrorText>{loadError}</ErrorText>
  if (!arc) return null

  return (
    <>
      <HStack
        as="nav"
        aria-label="Where this sits"
        wrap="wrap"
        align="baseline"
        gap="2"
        mb="4"
        fontSize="sm"
      >
        <ChakraLink asChild color="fg.muted" _hover={{ color: 'fg' }}>
          <Link to="/arcs">Arcs</Link>
        </ChakraLink>
        {/* Not a path: an arc can be filed into several others at once, so
            this reads as a list of what it supports rather than a trail. */}
        {supports.length > 0 && (
          <HStack wrap="wrap" align="baseline" gap="1">
            <Text color="fg.subtle" me="1">
              supports
            </Text>
            {supports.map((up, i) => (
              <Text as="span" key={up.id} color="fg.muted">
                {i > 0 && ', '}
                <ChakraLink asChild color="fg.muted" _hover={{ color: 'fg' }}>
                  <Link to={`/arcs/${up.id}`}>{up.title}</Link>
                </ChakraLink>
              </Text>
            ))}
          </HStack>
        )}
      </HStack>

      <Header
        arc={arc}
        onSaved={(saved) => {
          setArc(saved)
          void load()
        }}
      />

      <Grid
        templateColumns={{ base: 'minmax(0, 1fr)', lg: 'minmax(0, 1fr) minmax(0, 1fr)' }}
        gap="6"
        alignItems="start"
      >
        <Narrative
          arcId={id}
          entries={entries}
          onAdded={(e) => setEntries((all) => [...all, e])}
        />
        <Evidence
          arcId={id}
          events={events}
          arcs={evidenceArcs}
          onEventsChanged={setEvents}
          onArcsChanged={setEvidenceArcs}
        />
      </Grid>
    </>
  )
}

// ── header ───────────────────────────────────────────────────────────────

function Header({ arc, onSaved }: { arc: Arc; onSaved: (a: Arc) => void }) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(arc)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  function open() {
    setDraft(arc)
    setError(null)
    setEditing(true)
  }

  async function save(e: React.FormEvent) {
    e.preventDefault()
    setSaving(true)
    setError(null)
    try {
      // A whole-record replace: an emptied date field means "clear it", which
      // a merge could never express.
      const { arc: saved } = await updateArc(arc.id, {
        title: draft.title,
        status: draft.status,
        summary: draft.summary ?? '',
        started_at: draft.started_at ?? '',
        target_at: draft.target_at ?? '',
        ended_at: draft.ended_at ?? '',
      })
      onSaved(saved)
      setEditing(false)
    } catch (err) {
      setError(message(err))
    } finally {
      setSaving(false)
    }
  }

  if (!editing) {
    return (
      <Card.Root as="section" mb="6">
        <Card.Body>
          <HStack align="baseline" justify="space-between" gap="4">
            <Heading as="h1" fontSize="xl" fontWeight="normal">
              {arc.title}
            </Heading>
            <Button variant="outline" size="sm" onClick={open}>
              Edit
            </Button>
          </HStack>

          <HStack mt="2" wrap="wrap" gap="2">
            <Badge
              variant="outline"
              fontFamily="mono"
              fontSize="xs"
              colorPalette={STATUS_PALETTE[arc.status] ?? 'gray'}
            >
              {arc.status}
            </Badge>
            {arc.started_at && (
              <Text fontSize="xs" color="fg.subtle">
                from {arc.started_at}
              </Text>
            )}
            {arc.target_at && (
              <Text fontSize="xs" color="fg.subtle">
                target {arc.target_at}
              </Text>
            )}
            {arc.ended_at && (
              <Text fontSize="xs" color="fg.subtle">
                ended {arc.ended_at}
              </Text>
            )}
          </HStack>

          {arc.summary && (
            <Text mt="3" color="fg.muted" whiteSpace="pre-wrap">
              {arc.summary}
            </Text>
          )}
        </Card.Body>
      </Card.Root>
    )
  }

  return (
    <Card.Root as="section" mb="6">
      <Card.Body as="form" onSubmit={save}>
        <Stack gap="4">
          <Field.Root required>
            <Field.Label>Title</Field.Label>
            <Input
              value={draft.title}
              onChange={(e) => setDraft({ ...draft, title: e.target.value })}
            />
          </Field.Root>

          <Stack direction={{ base: 'column', sm: 'row' }} gap="4">
            <SelectField
              label="Status"
              value={draft.status}
              onChange={(value) => setDraft({ ...draft, status: value as Arc['status'] })}
              items={STATUSES}
              flex="1"
            />
            <DateField
              label="Started"
              value={draft.started_at ?? ''}
              onChange={(value) => setDraft({ ...draft, started_at: value })}
              flex="1"
            />
            <DateField
              label="Target"
              value={draft.target_at ?? ''}
              onChange={(value) => setDraft({ ...draft, target_at: value })}
              flex="1"
            />
            <DateField
              label="Ended"
              value={draft.ended_at ?? ''}
              onChange={(value) => setDraft({ ...draft, ended_at: value })}
              flex="1"
            />
          </Stack>

          <Field.Root>
            <Field.Label>What it is</Field.Label>
            <Textarea
              rows={3}
              value={draft.summary ?? ''}
              onChange={(e) => setDraft({ ...draft, summary: e.target.value })}
            />
          </Field.Root>

          {error && <ErrorText>{error}</ErrorText>}

          <HStack gap="3">
            <Button type="submit" loading={saving} disabled={!draft.title.trim()}>
              Save
            </Button>
            <Button variant="outline" type="button" onClick={() => setEditing(false)}>
              Cancel
            </Button>
          </HStack>
        </Stack>
      </Card.Body>
    </Card.Root>
  )
}

// ── narrative ────────────────────────────────────────────────────────────

function Narrative({
  arcId,
  entries,
  onAdded,
}: {
  arcId: string
  entries: ArcEntry[]
  onAdded: (entry: ArcEntry) => void
}) {
  const [body, setBody] = useState('')
  const [kind, setKind] = useState('none')
  const [occurredAt, setOccurredAt] = useState(() => toDateInputValue(new Date()))
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setSaving(true)
    setError(null)
    try {
      const { entry } = await addArcEntry(arcId, {
        kind: kind === 'none' ? undefined : kind,
        body,
        occurred_at: new Date(occurredAt).toISOString(),
      })
      onAdded(entry)
      setBody('')
      setKind('none')
      setOccurredAt(toDateInputValue(new Date()))
    } catch (err) {
      setError(message(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card.Root as="section" aria-labelledby="narrative-heading">
      <Card.Body>
        <HStack id="narrative-heading" as="h2" gap="3" fontSize="lg" fontWeight="medium">
          <Text>Log</Text>
          {entries.length > 0 && (
            <Text fontSize="sm" color="fg.subtle" fontVariantNumeric="tabular-nums">
              {entries.length}
            </Text>
          )}
        </HStack>

        {entries.length > 0 && (
          <Stack as="ol" listStyleType="none" mt="6" gap="3">
            {entries.map((entry) => (
              <Box as="li" key={entry.id} ps="4" borderStartWidth="2px">
                <HStack gap="2">
                  <Text fontSize="xs" color="fg.subtle" fontVariantNumeric="tabular-nums">
                    {dayLabel(entry.occurred_at)}
                  </Text>
                  {entry.kind && (
                    <Badge variant="outline" fontFamily="mono" fontSize="xs">
                      {entry.kind}
                    </Badge>
                  )}
                </HStack>
                <Text mt="1" fontSize="sm" whiteSpace="pre-wrap">
                  {entry.body}
                </Text>
              </Box>
            ))}
          </Stack>
        )}

        <Stack as="form" onSubmit={submit} gap="4" mt="6">
          <Field.Root required>
            <Textarea
              rows={3}
              value={body}
              onChange={(e) => setBody(e.target.value)}
              placeholder="I think the cutover fails on the session store. Betting we lose a week."
            />
          </Field.Root>

          <Stack direction={{ base: 'column', sm: 'row' }} gap="4">
            <SelectField
              label={
                <>
                  Kind{' '}
                  <Text as="span" color="fg.subtle">
                    optional
                  </Text>
                </>
              }
              value={kind}
              onChange={setKind}
              items={ENTRY_KIND_ITEMS}
              flex="1"
            />
            <DateField label="When" value={occurredAt} onChange={setOccurredAt} flex="1" />
          </Stack>

          {error && <ErrorText>{error}</ErrorText>}

          <Button type="submit" alignSelf="start" loading={saving} disabled={!body.trim()}>
            Add entry
          </Button>
        </Stack>
      </Card.Body>
    </Card.Root>
  )
}

// ── evidence ─────────────────────────────────────────────────────────────

function Evidence({
  arcId,
  events,
  arcs,
  onEventsChanged,
  onArcsChanged,
}: {
  arcId: string
  events: ArcEvent[]
  arcs: ArcRow[]
  onEventsChanged: (events: ArcEvent[]) => void
  onArcsChanged: (arcs: ArcRow[]) => void
}) {
  const [adding, setAdding] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function unfileEvent(eventId: string) {
    setError(null)
    try {
      await detachEvent(arcId, eventId)
      onEventsChanged(events.filter((e) => e.id !== eventId))
    } catch (err) {
      setError(message(err))
    }
  }

  async function unfileArc(id: string) {
    setError(null)
    try {
      await detachArc(arcId, id)
      onArcsChanged(arcs.filter((a) => a.id !== id))
    } catch (err) {
      setError(message(err))
    }
  }

  const total = events.length + arcs.length

  return (
    <Card.Root as="section" aria-labelledby="evidence-heading">
      <Card.Body>
        <HStack id="evidence-heading" as="h2" gap="3" fontSize="lg" fontWeight="medium">
          <Text>Evidence</Text>
          {total > 0 && (
            <Text fontSize="sm" color="fg.subtle" fontVariantNumeric="tabular-nums">
              {total}
            </Text>
          )}
          <Button
            variant="outline"
            size="sm"
            ms="auto"
            onClick={() => setAdding((open) => !open)}
          >
            {adding ? 'Done' : 'Add evidence'}
          </Button>
        </HStack>

        {adding && (
          <AddEvidence
            arcId={arcId}
            onEventsAttached={onEventsChanged}
            onArcsAttached={onArcsChanged}
          />
        )}

        {error && (
          <Box mt="3">
            <ErrorText>{error}</ErrorText>
          </Box>
        )}

        {total === 0 && !adding && (
          <Text mt="3" fontSize="sm" color="fg.muted">
            Nothing filed yet. Add events, or file another arc in — a line of work can be
            evidence for this one and still stand on its own.
          </Text>
        )}

        {/* Arcs first: they are the larger claim, and reading them before the
            raw events is the order the export will want too. */}
        <Stack as="ul" listStyleType="none" mt="6" gap="3">
          {arcs.map((child) => (
            <Box
              as="li"
              key={child.id}
              p="3"
              borderRadius="l2"
              bg={isEmpty(child) ? 'transparent' : 'bg.muted'}
              borderWidth={isEmpty(child) ? '1px' : undefined}
              borderStyle="dashed"
            >
              <HStack wrap="wrap" gap="2">
                <Badge variant="outline" fontFamily="mono" fontSize="xs">
                  arc
                </Badge>
                <Text fontSize="xs" color="fg.subtle">
                  {isEmpty(child)
                    ? 'nothing in it yet'
                    : `${child.event_count} ${child.event_count === 1 ? 'event' : 'events'}`}
                </Text>
                {child.supports_count > 1 && (
                  <Text fontSize="xs" color="fg.subtle">
                    also supports {child.supports_count - 1} other
                    {child.supports_count - 1 === 1 ? '' : 's'}
                  </Text>
                )}
                <UnfileButton onClick={() => void unfileArc(child.id)} />
              </HStack>
              <Text mt="2">
                <ChakraLink asChild color="fg">
                  <Link to={`/arcs/${child.id}`}>{child.title}</Link>
                </ChakraLink>
              </Text>
            </Box>
          ))}

          {events.map((event) => (
            <Box as="li" key={event.id} p="3" bg="bg.muted" borderRadius="l2">
              <HStack wrap="wrap" gap="2">
                <Badge variant="outline" fontFamily="mono" fontSize="xs">
                  {event.kind}
                </Badge>
                <Text fontSize="xs" color="fg.subtle">
                  {dayLabel(event.occurred_at)}
                </Text>
                <Text fontSize="xs" color="fg.subtle">
                  {event.source_label}
                </Text>
                <UnfileButton onClick={() => void unfileEvent(event.id)} />
              </HStack>
              <Text mt="2">
                {event.url ? (
                  <ChakraLink href={event.url} target="_blank" rel="noreferrer" color="fg">
                    {event.title}
                  </ChakraLink>
                ) : (
                  event.title
                )}
              </Text>
              {event.other_arcs && event.other_arcs.length > 0 && (
                <Text mt="2" fontSize="sm" color="fg.muted">
                  also in {event.other_arcs.join(', ')}
                </Text>
              )}
            </Box>
          ))}
        </Stack>
      </Card.Body>
    </Card.Root>
  )
}

function UnfileButton({ onClick }: { onClick: () => void }) {
  return (
    <Button
      variant="plain"
      size="xs"
      height="auto"
      px="0"
      ms="auto"
      color="fg.subtle"
      textDecoration="underline"
      onClick={onClick}
    >
      Unfile
    </Button>
  )
}

// ── add evidence ─────────────────────────────────────────────────────────

function AddEvidence({
  arcId,
  onEventsAttached,
  onArcsAttached,
}: {
  arcId: string
  onEventsAttached: (events: ArcEvent[]) => void
  onArcsAttached: (arcs: ArcRow[]) => void
}) {
  // Two kinds of evidence, one panel. Tabs rather than two buttons, because
  // the searching and ticking are identical and only the rows differ.
  const [kind, setKind] = useState<'events' | 'arcs'>('events')
  const [arcCandidates_, setArcCandidates] = useState<ArcRow[]>([])
  const [refused, setRefused] = useState<string[]>([])
  const [query, setQuery] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [candidates, setCandidates] = useState<ArcEvent[]>([])
  const [picked, setPicked] = useState<Set<string>>(() => new Set())
  const [searching, setSearching] = useState(false)
  const [attaching, setAttaching] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [attached, setAttached] = useState<number | null>(null)

  // A generation counter, so a slow early response cannot overwrite the
  // results of a later, narrower search.
  const generation = useRef(0)

  useEffect(() => {
    const run = ++generation.current
    const timer = setTimeout(async () => {
      setSearching(true)
      try {
        if (kind === 'arcs') {
          const { arcs } = await arcCandidateArcs(arcId, query.trim() || undefined)
          if (generation.current !== run) return
          setArcCandidates(arcs)
        } else {
          const { events } = await arcCandidates(arcId, {
            q: query.trim() || undefined,
            from: from || undefined,
            to: to || undefined,
          })
          if (generation.current !== run) return
          setCandidates(events)
        }
        setError(null)
      } catch (err) {
        if (generation.current === run) setError(message(err))
      } finally {
        if (generation.current === run) setSearching(false)
      }
    }, 250)
    return () => clearTimeout(timer)
  }, [arcId, kind, query, from, to])

  // Switching kind clears a selection that no longer means anything.
  useEffect(() => {
    setPicked(new Set())
    setRefused([])
  }, [kind])

  const rows = kind === 'arcs' ? arcCandidates_ : candidates
  const visibleIds = useMemo(() => rows.map((c) => c.id), [rows])
  const allPicked = visibleIds.length > 0 && visibleIds.every((id) => picked.has(id))

  function toggle(id: string) {
    setPicked((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  function toggleAll() {
    setPicked((current) => {
      const next = new Set(current)
      if (allPicked) visibleIds.forEach((id) => next.delete(id))
      else visibleIds.forEach((id) => next.add(id))
      return next
    })
  }

  async function attach() {
    setAttaching(true)
    setError(null)
    setRefused([])
    try {
      if (kind === 'arcs') {
        const result = await attachArcs(arcId, [...picked])
        onArcsAttached(result.evidence_arcs)
        setAttached(result.attached)
        setRefused(result.refused)
        setPicked(new Set())
        generation.current++
        const { arcs } = await arcCandidateArcs(arcId, query.trim() || undefined)
        setArcCandidates(arcs)
        return
      }
      const { attached, events } = await attachEvents(arcId, [...picked])
      onEventsAttached(events)
      setAttached(attached)
      setPicked(new Set())
      // Re-run the search so the newly filed events leave the list.
      generation.current++
      const { events: remaining } = await arcCandidates(arcId, {
        q: query.trim() || undefined,
        from: from || undefined,
        to: to || undefined,
      })
      setCandidates(remaining)
    } catch (err) {
      setError(message(err))
    } finally {
      setAttaching(false)
    }
  }

  const noun = (n: number) =>
    kind === 'arcs' ? (n === 1 ? 'arc' : 'arcs') : n === 1 ? 'event' : 'events'

  // The same body under both tabs; only one is mounted at a time, and every
  // piece of its state lives in this component.
  const body = (
    <Stack gap="4">
      <Field.Root>
        <Field.Label>
          {kind === 'arcs' ? 'Search your arcs' : 'Search your events'}
        </Field.Label>
        <Input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="auth, migration, oncall…"
          autoFocus
        />
      </Field.Root>

      {kind === 'events' && (
        <Stack direction={{ base: 'column', sm: 'row' }} gap="4">
          <DateField
            label={
              <>
                From{' '}
                <Text as="span" color="fg.subtle">
                  optional
                </Text>
              </>
            }
            value={from}
            onChange={setFrom}
            flex="1"
          />
          <DateField
            label={
              <>
                To{' '}
                <Text as="span" color="fg.subtle">
                  optional
                </Text>
              </>
            }
            value={to}
            onChange={setTo}
            flex="1"
          />
        </Stack>
      )}

      <HStack justify="space-between" gap="3">
        <Button
          variant="plain"
          size="xs"
          height="auto"
          px="0"
          color="fg.subtle"
          textDecoration="underline"
          onClick={toggleAll}
          disabled={visibleIds.length === 0}
        >
          {allPicked ? 'Clear all' : `Select all ${visibleIds.length}`}
        </Button>
        <Text fontSize="sm" color="fg.subtle" fontVariantNumeric="tabular-nums">
          {searching ? 'searching…' : `${rows.length} unfiled`}
        </Text>
      </HStack>

      {error && <ErrorText>{error}</ErrorText>}

      {attached !== null && !error && (
        <Text fontSize="sm" color="fg.muted">
          Filed {attached} {noun(attached)}.
        </Text>
      )}

      {/* A loop is refused per pick, so the rest of the batch still lands. */}
      {refused.map((why) => (
        <Alert.Root key={why} status="warning">
          <Alert.Indicator />
          <Alert.Title>Not filed: {why}.</Alert.Title>
        </Alert.Root>
      ))}

      {!searching && rows.length === 0 && (
        <Text fontSize="sm" color="fg.muted">
          {kind === 'arcs'
            ? 'No other arcs to file in. Anything already here, or anything this arc sits inside, is left out.'
            : 'Nothing matches. Widen the search, or record it by hand on the timeline — the work that never reaches an API is usually the work worth claiming.'}
        </Text>
      )}

      {/* Capped so a wide search cannot push the file button off the screen. */}
      <Stack as="ul" listStyleType="none" gap="1" maxH="22rem" overflowY="auto">
        {kind === 'arcs'
          ? arcCandidates_.map((candidate) => (
              <Candidate
                key={candidate.id}
                checked={picked.has(candidate.id)}
                onChange={() => toggle(candidate.id)}
                title={candidate.title}
                meta={
                  <>
                    {candidate.status} · {candidate.event_count}{' '}
                    {candidate.event_count === 1 ? 'event' : 'events'}
                    {candidate.arc_count > 0 && <> · holds {candidate.arc_count}</>}
                    {candidate.supports_count > 0 && (
                      <> · already supports {candidate.supports_count}</>
                    )}
                  </>
                }
              />
            ))
          : candidates.map((event) => (
              <Candidate
                key={event.id}
                checked={picked.has(event.id)}
                onChange={() => toggle(event.id)}
                title={event.title}
                meta={
                  <>
                    {dayLabel(event.occurred_at)} · {event.kind} · {event.source_label}
                    {event.other_arcs && event.other_arcs.length > 0 && (
                      <> · already in {event.other_arcs.join(', ')}</>
                    )}
                  </>
                }
              />
            ))}
      </Stack>

      <Button
        alignSelf="start"
        onClick={() => void attach()}
        loading={attaching}
        loadingText="Filing…"
        disabled={picked.size === 0}
      >
        File {picked.size} {noun(picked.size)}
      </Button>
    </Stack>
  )

  return (
    <Box mt="6" p="4" bg="bg" borderWidth="1px" borderRadius="l2">
      <Tabs.Root
        value={kind}
        onValueChange={(details) => setKind(details.value as 'events' | 'arcs')}
        size="sm"
        lazyMount
        unmountOnExit
      >
        <Tabs.List aria-label="What to file">
          <Tabs.Trigger value="events">Events</Tabs.Trigger>
          <Tabs.Trigger value="arcs">Other arcs</Tabs.Trigger>
        </Tabs.List>
        <Tabs.Content value="events">{body}</Tabs.Content>
        <Tabs.Content value="arcs">{body}</Tabs.Content>
      </Tabs.Root>
    </Box>
  )
}

function Candidate({
  checked,
  onChange,
  title,
  meta,
}: {
  checked: boolean
  onChange: () => void
  title: string
  meta: ReactNode
}) {
  return (
    <Box as="li">
      <Checkbox.Root
        checked={checked}
        onCheckedChange={onChange}
        alignItems="start"
        gap="3"
        p="2"
        w="full"
        borderRadius="l2"
        cursor="pointer"
        _hover={{ bg: 'bg.muted' }}
      >
        <Checkbox.HiddenInput />
        <Checkbox.Control mt="1" flex="none" />
        <Checkbox.Label display="grid" gap="1" minW="0" fontWeight="normal">
          <Text fontSize="sm">{title}</Text>
          <Text fontSize="xs" color="fg.subtle">
            {meta}
          </Text>
        </Checkbox.Label>
      </Checkbox.Root>
    </Box>
  )
}
