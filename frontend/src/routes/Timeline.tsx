import {
  Alert,
  Badge,
  Box,
  Button,
  Card,
  Heading,
  HStack,
  Link,
  Spinner,
  Stack,
  Text,
} from '@chakra-ui/react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useOutletContext } from 'react-router-dom'

import { deleteEvent, listEvents, restoreEvent, type EventRow } from '../api'
import ConfirmDialog, { type Confirmation } from '../components/ConfirmDialog'
import ReviewBanner from '../components/ReviewBanner'
import type { ShellContext } from '../layout/AppShell'

function dayKey(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, {
    weekday: 'long',
    year: 'numeric',
    month: 'long',
    day: 'numeric',
  })
}

function timeLabel(iso: string): string {
  return new Date(iso).toLocaleTimeString(undefined, {
    hour: '2-digit',
    minute: '2-digit',
  })
}

export default function Timeline() {
  const [events, setEvents] = useState<EventRow[]>([])
  const [loadError, setLoadError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  // Deleted events stay on screen as an undo line until the next load. A soft
  // delete you cannot reverse is just a delete with extra steps.
  const [removed, setRemoved] = useState<Map<string, number>>(() => new Map())
  const [busy, setBusy] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [confirming, setConfirming] = useState<Confirmation | null>(null)

  // Capture happens in the sidebar, on whatever page you are on, so the count
  // of saves is what tells this page it is out of date.
  const { captures } = useOutletContext<ShellContext>()

  // A wide default window: this is a record of work, so the useful question is
  // "what happened this year", not "what happened today".
  const window = useMemo(() => {
    const to = new Date()
    to.setDate(to.getDate() + 1)
    const from = new Date()
    from.setFullYear(from.getFullYear() - 1)
    return {
      from: from.toISOString().slice(0, 10),
      to: to.toISOString().slice(0, 10),
    }
  }, [])

  const refresh = useCallback(async () => {
    try {
      const { events } = await listEvents(window.from, window.to)
      setEvents(events)
      setLoadError(null)
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : String(err))
    } finally {
      setLoading(false)
    }
  }, [window])

  useEffect(() => {
    void refresh()
  }, [refresh, captures])

  // Errors are left to throw: the confirm dialog reports them, and stays open
  // so the person can try again.
  async function remove(id: string) {
    setBusy(id)
    setActionError(null)
    try {
      const { filed_in } = await deleteEvent(id)
      setRemoved((current) => new Map(current).set(id, filed_in))
    } finally {
      setBusy(null)
    }
  }

  async function undo(id: string) {
    setBusy(id)
    setActionError(null)
    try {
      await restoreEvent(id)
      setRemoved((current) => {
        const next = new Map(current)
        next.delete(id)
        return next
      })
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  const grouped = useMemo(() => {
    const days = new Map<string, EventRow[]>()
    for (const event of events) {
      const key = dayKey(event.occurred_at)
      const bucket = days.get(key)
      if (bucket) bucket.push(event)
      else days.set(key, [event])
    }
    return [...days.entries()]
  }, [events])

  return (
    <>
      <Heading as="h1" fontSize="xl" fontWeight="normal" mb="6">
        Timeline
      </Heading>

      <ReviewBanner onAdded={() => void refresh()} />

      <Card.Root as="section" aria-labelledby="timeline-heading">
        <Card.Body>
          <HStack id="timeline-heading" as="h2" gap="3" fontSize="lg" fontWeight="medium">
            <Text>The last year</Text>
            {/* Deleted rows are still on screen as undo lines, but they are not
              events any more, so the count should not include them. */}
            {events.length - removed.size > 0 && (
              <Text fontSize="sm" color="fg.subtle" fontVariantNumeric="tabular-nums">
                {events.length - removed.size}
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
          {actionError && (
            <Alert.Root status="error" mt="3">
              <Alert.Indicator />
              <Alert.Title>{actionError}</Alert.Title>
            </Alert.Root>
          )}

          {!loading && !loadError && events.length === 0 && (
            <Text mt="3" fontSize="sm" color="fg.muted">
              Nothing recorded yet. Hit <Text as="strong">Record something</Text> in the
              sidebar, connect a source, or post to an ingest endpoint from a script.
            </Text>
          )}

          <Stack as="ol" listStyleType="none" mt="6" gap="6">
            {grouped.map(([day, dayEvents]) => (
              <Box as="li" key={day}>
                <Heading
                  as="h3"
                  fontSize="sm"
                  fontWeight="medium"
                  color="fg.muted"
                  pb="2"
                  borderBottomWidth="1px"
                >
                  {day}
                </Heading>

                <Stack as="ul" listStyleType="none" mt="3" gap="3">
                  {dayEvents.map((event) => {
                    const filedIn = removed.get(event.id)
                    if (filedIn !== undefined) {
                      return (
                        <HStack
                          as="li"
                          key={event.id}
                          wrap="wrap"
                          align="baseline"
                          gap="3"
                          p="3"
                          borderWidth="1px"
                          borderStyle="dashed"
                          borderRadius="l2"
                        >
                          <Text fontSize="sm" color="fg.muted">
                            Deleted “{event.title}”
                            {filedIn > 0 &&
                              `, and unfiled from ${filedIn} ${filedIn === 1 ? 'arc' : 'arcs'}`}
                            . It will not come back on the next sync.
                          </Text>
                          <Button
                            variant="plain"
                            size="xs"
                            height="auto"
                            px="0"
                            color="fg.subtle"
                            textDecoration="underline"
                            onClick={() => void undo(event.id)}
                            disabled={busy === event.id}
                          >
                            Undo
                          </Button>
                        </HStack>
                      )
                    }
                    return (
                      <Box as="li" key={event.id} p="3" bg="bg.muted" borderRadius="l2">
                        <HStack wrap="wrap" gap="2" fontSize="sm">
                          <Badge variant="outline" fontFamily="mono" fontSize="xs">
                            {event.kind}
                          </Badge>
                          <Text fontSize="xs" color="fg.subtle">
                            {timeLabel(event.occurred_at)}
                          </Text>
                          <Text fontSize="xs" color="fg.subtle">
                            {event.source_label}
                          </Text>
                          <Button
                            variant="ghost"
                            size="xs"
                            ms="auto"
                            color="fg.subtle"
                            onClick={() =>
                              setConfirming({
                                title: 'Delete this event?',
                                body: (
                                  <>
                                    “{event.title}” leaves your timeline, and a re-sync will
                                    not bring it back.
                                  </>
                                ),
                                confirmLabel: 'Delete it',
                                onConfirm: () => remove(event.id),
                              })
                            }
                            disabled={busy === event.id}
                          >
                            ×
                          </Button>
                        </HStack>

                        <Text mt="2">
                          {event.url ? (
                            <Link
                              href={event.url}
                              target="_blank"
                              rel="noreferrer"
                              color="fg"
                            >
                              {event.title}
                            </Link>
                          ) : (
                            event.title
                          )}
                        </Text>

                        {typeof event.payload?.note === 'string' && (
                          <Text mt="2" fontSize="sm" color="fg.muted" whiteSpace="pre-wrap">
                            {event.payload.note}
                          </Text>
                        )}
                      </Box>
                    )
                  })}
                </Stack>
              </Box>
            ))}
          </Stack>
        </Card.Body>
      </Card.Root>

      <ConfirmDialog request={confirming} onClose={() => setConfirming(null)} />
    </>
  )
}
