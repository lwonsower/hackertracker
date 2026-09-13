import { useEffect, useRef } from 'react'

/**
 * A thin wrapper over the native <dialog>, which brings Escape-to-close, focus
 * containment and an inert background with no JavaScript of ours.
 */
export default function Modal({
  open,
  onClose,
  labelledBy,
  children,
}: {
  open: boolean
  onClose: () => void
  labelledBy: string
  children: React.ReactNode
}) {
  const ref = useRef<HTMLDialogElement>(null)

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    if (open && !dialog.open) {
      dialog.showModal()
      // React sets autoFocus by calling focus() at mount, not by writing the
      // attribute, so showModal's own focus step never sees it and lands on
      // the close button instead.
      dialog.querySelector<HTMLElement>('[data-autofocus]')?.focus()
    }
    if (!open && dialog.open) dialog.close()
  }, [open])

  return (
    <dialog
      ref={ref}
      className="modal m-auto"
      aria-labelledby={labelledBy}
      // Escape fires close without going through the button, so the parent's
      // state is told here rather than only where it is dismissed.
      onClose={onClose}
      // A click that lands on the dialog element itself is a click on the
      // backdrop: the content sits in a child, which stops it.
      onClick={(e) => {
        if (e.target === ref.current) onClose()
      }}
    >
      <div className="modal__panel">{children}</div>
    </dialog>
  )
}
