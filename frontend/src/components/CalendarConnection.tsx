import { Badge, Button, Card, HStack, Text } from '@chakra-ui/react'
import { useCallback, useEffect, useState } from 'react'

import { calendarStatus, disconnectCalendar, type CalendarStatus } from '../api'
import ConfirmDialog, { type Confirmation } from './ConfirmDialog'

function reviewed(iso?: string): string {
  if (!iso) return 'never reviewed'
  const days = Math.round((Date.now() - new Date(iso).getTime()) / 86_400_000)
  if (days <= 0) return 'reviewed today'
  if (days === 1) return 'reviewed yesterday'
  if (days < 30) return `reviewed ${days} days ago`
  return `reviewed ${Math.round(days / 30)} months ago`
}

/**
 * The calendar's home on the Sources page. The review banner is where you use
 * the connection; this is where you can see it exists and take it away.
 */
export default function CalendarConnection() {
  const [status, setStatus] = useState<CalendarStatus | null>(null)
  const [confirming, setConfirming] = useState<Confirmation | null>(null)

  const load = useCallback(async () => {
    try {
      setStatus(await calendarStatus())
    } catch {
      setStatus(null)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  if (!status?.available) return null

  async function handleDisconnect() {
    await disconnectCalendar()
    await load()
  }

  const disconnectRequest: Confirmation = {
    title: 'Disconnect Google Calendar?',
    body: 'This removes our access to your calendar. Meetings you already recorded stay on your timeline, with the notes you wrote.',
    confirmLabel: 'Yes, disconnect',
    onConfirm: handleDisconnect,
  }

  return (
    <Card.Root as="section" aria-labelledby="calendar-heading">
      <Card.Body>
        <HStack id="calendar-heading" as="h2" gap="3" fontSize="lg" fontWeight="medium">
          <Text>Google Calendar</Text>
          {status.connected && (
            <Badge variant="outline" fontFamily="mono" fontSize="xs">
              review
            </Badge>
          )}
        </HStack>

        {!status.connected ? (
          <>
            <Text mt="3" fontSize="sm" color="fg.muted">
              {status.disconnected
                ? 'Disconnected. The meetings you recorded are still on your timeline.'
                : 'Meetings are never synced. Each week you are shown what happened and you choose what to keep.'}
            </Text>
            <Button asChild variant="outline" size="sm" mt="4" alignSelf="start">
              <a href="/api/calendar/connect">
                {status.disconnected ? 'Reconnect' : 'Connect'}
              </a>
            </Button>
          </>
        ) : (
          <>
            <Text mt="2" fontSize="xs" color="fg.subtle">
              {status.account}
              {` · ${reviewed(status.last_reviewed_at)}`}
            </Text>
            <Text mt="3" fontSize="sm" color="fg.muted">
              Read-only. Nothing from your calendar is stored unless you pick it during a
              review.
            </Text>

            <div className="flex gap-4 w-full">
              <Button asChild variant="outline" size="sm" my="4" alignSelf="start">
                <a href="/?calendar=review">Review meetings</a>
              </Button>
              <Button
                variant="outline"
                size="sm"
                alignSelf="start"
                onClick={() => setConfirming(disconnectRequest)}
              >
                Disconnect
              </Button>
            </div>
          </>
        )}
      </Card.Body>

      <ConfirmDialog request={confirming} onClose={() => setConfirming(null)} />
    </Card.Root>
  )
}
