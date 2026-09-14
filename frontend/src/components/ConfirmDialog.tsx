import { Alert, Button, Dialog, Portal, Stack } from '@chakra-ui/react'
import type { ReactNode } from 'react'
import { useRef, useState } from 'react'

/**
 * What a caller hands over when it wants something confirmed. The action is a
 * promise so the dialog can hold the button until it settles, and report a
 * failure where the person is looking rather than behind the closed dialog.
 */
export type Confirmation = {
  title: string
  body: ReactNode
  confirmLabel: string
  onConfirm: () => Promise<unknown> | unknown
}

/**
 * One dialog for every destructive action. Open it by putting a Confirmation
 * in state; it closes itself once the action succeeds.
 */
export default function ConfirmDialog({
  request,
  onClose,
}: {
  request: Confirmation | null
  onClose: () => void
}) {
  const [working, setWorking] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Cancel takes the focus: the destructive button should never be one stray
  // keypress away.
  const cancelRef = useRef<HTMLButtonElement>(null)

  function close() {
    setError(null)
    onClose()
  }

  async function confirm() {
    if (!request) return
    setWorking(true)
    setError(null)
    try {
      await request.onConfirm()
      close()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setWorking(false)
    }
  }

  return (
    <Dialog.Root
      role="alertdialog"
      open={request !== null}
      onOpenChange={(details) => !details.open && close()}
      initialFocusEl={() => cancelRef.current}
    >
      <Portal>
        <Dialog.Backdrop />
        <Dialog.Positioner>
          <Dialog.Content maxW="md">
            <Dialog.Header>
              <Dialog.Title>{request?.title}</Dialog.Title>
            </Dialog.Header>

            <Dialog.Body>
              <Stack gap="4">
                <Dialog.Description color="fg.muted" fontSize="sm">
                  {request?.body}
                </Dialog.Description>

                {error && (
                  <Alert.Root status="error">
                    <Alert.Indicator />
                    <Alert.Title>{error}</Alert.Title>
                  </Alert.Root>
                )}
              </Stack>
            </Dialog.Body>

            <Dialog.Footer>
              <Button ref={cancelRef} variant="outline" onClick={close} disabled={working}>
                Cancel
              </Button>
              <Button colorPalette="red" loading={working} onClick={() => void confirm()}>
                {request?.confirmLabel}
              </Button>
            </Dialog.Footer>
          </Dialog.Content>
        </Dialog.Positioner>
      </Portal>
    </Dialog.Root>
  )
}
