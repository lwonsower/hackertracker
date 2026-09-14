import {
  Button,
  Combobox,
  Dialog,
  Field,
  Input,
  Portal,
  Stack,
  Textarea,
  useListCollection,
} from '@chakra-ui/react'
import { useRef, useState } from 'react'

import { createEvent } from '../api'
import DateField from './DateField'

// Suggestions, not a fixed list — the field stays free text, because the kinds
// that matter here are the ones no API will ever hand you.
const KIND_SUGGESTIONS = [
  'note',
  'mentoring',
  'design_review',
  'doc_published',
  'incident_resolved',
  'talk_given',
  'decision',
]

function today(): string {
  const now = new Date()
  const offset = now.getTimezoneOffset() * 60_000
  return new Date(now.getTime() - offset).toISOString().slice(0, 10)
}

function now(): string {
  const at = new Date()
  const offset = at.getTimezoneOffset() * 60_000
  return new Date(at.getTime() - offset).toISOString().slice(11, 16)
}

export default function CaptureDialog({
  open,
  onClose,
  onSaved,
}: {
  open: boolean
  onClose: () => void
  onSaved: () => void
}) {
  const [title, setTitle] = useState('')
  const [kind, setKind] = useState('note')
  // Chakra's DatePicker is date-only — the documented "with time" pattern is a
  // date picker composed with a time input, so the two are kept separate here
  // and joined on submit.
  const [date, setDate] = useState(today)
  const [time, setTime] = useState(now)
  const [url, setUrl] = useState('')
  const [note, setNote] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const titleRef = useRef<HTMLInputElement>(null)

  const { collection, filter, reset } = useListCollection({
    initialItems: KIND_SUGGESTIONS,
    filter: (item, query) => item.toLowerCase().includes(query.toLowerCase()),
  })

  function clear() {
    setTitle('')
    setKind('note')
    setUrl('')
    setNote('')
    setDate(today())
    setTime(now())
    setError(null)
    reset()
  }

  // A half-typed capture is thrown away on close rather than kept: coming back
  // to a stale form with yesterday's timestamp is worse than retyping a line.
  function close() {
    clear()
    onClose()
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setSaving(true)
    setError(null)
    try {
      await createEvent({
        title,
        kind: kind.trim() || 'note',
        // The pair carries no zone; the Date round-trip attaches the browser's,
        // which is what the person meant.
        occurred_at: new Date(`${date}T${time || '00:00'}`).toISOString(),
        url: url.trim() || undefined,
        note: note.trim() || undefined,
      })
      onSaved()
      close()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog.Root
      open={open}
      onOpenChange={(details) => !details.open && close()}
      initialFocusEl={() => titleRef.current}
    >
      <Portal>
        <Dialog.Backdrop />
        <Dialog.Positioner>
          <Dialog.Content as="form" onSubmit={handleSubmit} maxW="lg">
            <Dialog.Header>
              <Dialog.Title>Record something</Dialog.Title>
            </Dialog.Header>

            <Dialog.Body>
              <Dialog.Description mb="4" color="fg.muted" fontSize="sm">
                The work that never reaches an API — mentoring, design review, the call you
                talked someone out of — only gets recorded if you write it down.
              </Dialog.Description>

              <Stack gap="4">
                <Field.Root required>
                  <Field.Label>
                    What happened <Field.RequiredIndicator />
                  </Field.Label>
                  <Input
                    ref={titleRef}
                    value={title}
                    onChange={(e) => setTitle(e.target.value)}
                    placeholder="Unblocked Jordan on the rollback plan"
                  />
                </Field.Root>

                <Field.Root>
                  <Combobox.Root
                    collection={collection}
                    inputValue={kind}
                    allowCustomValue
                    openOnClick
                    onInputValueChange={(details) => {
                      setKind(details.inputValue)
                      filter(details.inputValue)
                    }}
                    onValueChange={(details) => setKind(details.value[0] ?? '')}
                  >
                    <Combobox.Label>Kind</Combobox.Label>
                    <Combobox.Control>
                      <Combobox.Input placeholder="note" />
                      <Combobox.IndicatorGroup>
                        <Combobox.Trigger />
                      </Combobox.IndicatorGroup>
                    </Combobox.Control>
                    <Portal>
                      <Combobox.Positioner>
                        <Combobox.Content>
                          <Combobox.Empty>Use it as a new kind</Combobox.Empty>
                          {collection.items.map((item) => (
                            <Combobox.Item key={item} item={item}>
                              <Combobox.ItemText>{item}</Combobox.ItemText>
                              <Combobox.ItemIndicator />
                            </Combobox.Item>
                          ))}
                        </Combobox.Content>
                      </Combobox.Positioner>
                    </Portal>
                  </Combobox.Root>
                </Field.Root>

                <Stack direction={{ base: 'column', sm: 'row' }} gap="4">
                  <DateField label="When" value={date} onChange={setDate} flex="1" />

                  <Field.Root flex="1">
                    <Field.Label>Time</Field.Label>
                    <Input
                      type="time"
                      value={time}
                      onChange={(e) => setTime(e.target.value)}
                    />
                  </Field.Root>
                </Stack>

                <Field.Root>
                  <Field.Label>Link</Field.Label>
                  <Input
                    value={url}
                    onChange={(e) => setUrl(e.target.value)}
                    placeholder="https://"
                  />
                  <Field.HelperText>Optional.</Field.HelperText>
                </Field.Root>

                <Field.Root>
                  <Field.Label>Context</Field.Label>
                  <Textarea
                    rows={3}
                    value={note}
                    onChange={(e) => setNote(e.target.value)}
                    placeholder="Why it mattered, who it helped, what it unblocked."
                  />
                  <Field.HelperText>Optional.</Field.HelperText>
                </Field.Root>

                {error && (
                  <Field.Root invalid>
                    <Field.ErrorText>{error}</Field.ErrorText>
                  </Field.Root>
                )}
              </Stack>
            </Dialog.Body>

            <Dialog.Footer>
              <Button variant="outline" type="button" onClick={close}>
                Cancel
              </Button>
              <Button type="submit" loading={saving} disabled={!title.trim()}>
                Record it
              </Button>
            </Dialog.Footer>

            <Dialog.CloseTrigger asChild>
              <Button variant="ghost" size="sm" aria-label="Close">
                ×
              </Button>
            </Dialog.CloseTrigger>
          </Dialog.Content>
        </Dialog.Positioner>
      </Portal>
    </Dialog.Root>
  )
}
