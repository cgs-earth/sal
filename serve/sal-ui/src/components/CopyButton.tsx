import { useEffect, useState } from 'react'
import { CheckIcon, ClipboardIcon } from './icons'

type CopyButtonProps = {
  /** What the button puts on the clipboard. */
  text: string
  /** What it is, for the tooltip: "Copy the metadata file path". */
  label: string
  className?: string
}

/** An icon button that copies text to the clipboard and shows a check for a moment after. */
export function CopyButton({ text, label, className }: CopyButtonProps) {
  const [copied, setCopied] = useState(false)
  useEffect(() => {
    if (!copied) return
    const timer = window.setTimeout(() => setCopied(false), 1500)
    return () => window.clearTimeout(timer)
  }, [copied])
  const title = copied ? 'Copied' : label
  return (
    <button
      type="button"
      className={`icon-button copy-button${copied ? ' copied' : ''}${className ? ` ${className}` : ''}`}
      title={title}
      aria-label={title}
      onClick={() => {
        void navigator.clipboard.writeText(text).then(() => setCopied(true))
      }}
    >
      {copied ? <CheckIcon /> : <ClipboardIcon />}
    </button>
  )
}
