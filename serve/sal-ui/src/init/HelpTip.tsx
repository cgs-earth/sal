import { useEffect, useRef, useState, type ReactNode } from 'react'

/**
 * A ? button that opens a short note beside the control it sits next to. The
 * note closes on a click anywhere else or on Escape, so it never has to be
 * dismissed by hand; it is a note, not a dialog.
 */
export function HelpTip({ label, children }: { label: string; children: ReactNode }) {
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLSpanElement>(null)

  useEffect(() => {
    if (!open) return
    const onClick = (event: MouseEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false)
    }
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onClick)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onClick)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  return (
    <span className="help-tip" ref={root}>
      <button
        type="button"
        className={`button small help-button${open ? ' active' : ''}`}
        aria-label={`About ${label}`}
        aria-expanded={open}
        title={`About ${label}`}
        onClick={() => setOpen((value) => !value)}
      >
        ?
      </button>
      {open && (
        <span className="help-pop" role="note">
          {children}
        </span>
      )}
    </span>
  )
}
