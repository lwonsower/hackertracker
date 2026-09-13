import { useState } from 'react'

import { createEvent } from '../api'
import Modal from './Modal'

// Suggestions, not a fixed list — the input stays free text. The ones that
// matter most here are the kinds no API will ever hand you.
const KIND_SUGGESTIONS = [
  'note',
  'mentoring',
  'design_review',
  'doc_published',
  'incident_resolved',
  'talk_given',
  'decision',
]

/** Value for <input type="datetime-local">, which wants local time, no zone. */
function toLocalInputValue(date: Date): string {
  const offset = date.getTimezoneOffset() * 60_000
  return new Date(date.getTime() - offset).toISOString().slice(0, 16)
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
  const [occurredAt, setOccurredAt] = useState(() => toLocalInputValue(new Date()))
  const [url, setUrl] = useState('')
  const [note, setNote] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  function reset() {
    setTitle('')
    setKind('note')
    setUrl('')
    setNote('')
    setOccurredAt(toLocalInputValue(new Date()))
    setError(null)
  }

  // A half-typed capture is thrown away on close rather than kept: coming back
  // to a stale form with yesterday's timestamp is worse than retyping a line.
  function close() {
    reset()
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
        // datetime-local has no zone; the Date round-trip attaches the
        // browser's, which is what the user meant.
        occurred_at: new Date(occurredAt).toISOString(),
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
    <Modal open={open} onClose={close} labelledBy="capture-heading">
      <div className="modal__head">
        <h2 id="capture-heading" className="panel__title">
          Record something
        </h2>
        <button className="modal__close" onClick={close} aria-label="Close">
          ×
        </button>
      </div>

      <p className="panel__hint">
        The work that never reaches an API — mentoring, design review, the call you
        talked someone out of — only gets recorded if you write it down.
      </p>

      <form className="form" onSubmit={handleSubmit}>
        <label className="field">
          <span className="field__label">What happened</span>
          <input
            className="field__input"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="Unblocked Jordan on the rollback plan"
            required
            data-autofocus
          />
        </label>

        <div className="field-row">
          <label className="field">
            <span className="field__label">Kind</span>
            <input
              className="field__input"
              value={kind}
              onChange={(e) => setKind(e.target.value)}
              list="kind-suggestions"
            />
            <datalist id="kind-suggestions">
              {KIND_SUGGESTIONS.map((k) => (
                <option key={k} value={k} />
              ))}
            </datalist>
          </label>

          <label className="field">
            <span className="field__label">When</span>
            <input
              className="field__input"
              type="datetime-local"
              value={occurredAt}
              onChange={(e) => setOccurredAt(e.target.value)}
              required
            />
          </label>
        </div>

        <label className="field">
          <span className="field__label">
            Link <span className="field__optional">optional</span>
          </span>
          <input
            className="field__input"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://"
          />
        </label>

        <label className="field">
          <span className="field__label">
            Context <span className="field__optional">optional</span>
          </span>
          <textarea
            className="field__input field__input--area"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            rows={3}
            placeholder="Why it mattered, who it helped, what it unblocked."
          />
        </label>

        {error && <p className="alert">{error}</p>}

        <div className="modal__actions">
          <button className="button" type="submit" disabled={saving || !title.trim()}>
            {saving ? 'Saving…' : 'Record it'}
          </button>
          <button className="button button--quiet" type="button" onClick={close}>
            Cancel
          </button>
        </div>
      </form>
    </Modal>
  )
}
