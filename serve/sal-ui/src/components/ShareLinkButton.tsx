import { useEffect, useState } from 'react'
import { shareLink, type TabName } from '../routing'

type ShareLinkButtonProps = {
  tab: TabName
  /** Read lazily, so the link always carries whatever the editor holds right now. */
  query: () => string
  /** Added to the button's classes, for the host that places it. */
  className?: string
}

const LABELS = {
  idle: 'Copy a link that reopens this tab with the current query',
  copied: 'Link copied',
  empty: 'Nothing to share: the editor is empty',
} as const

/**
 * Copies a link to this tab with the current query URL encoded into it. Opening
 * the link loads that query back into the editor, which is how a query is
 * handed to someone else without pasting the text itself. It is an icon
 * button: a chain link, which turns into a check mark once the link is on the
 * clipboard.
 */
export function ShareLinkButton({ tab, query, className = '' }: ShareLinkButtonProps) {
  const [state, setState] = useState<'idle' | 'copied' | 'empty'>('idle')

  useEffect(() => {
    if (state === 'idle') return
    const timer = setTimeout(() => setState('idle'), 1600)
    return () => clearTimeout(timer)
  }, [state])

  const copy = async () => {
    const text = query().trim()
    if (!text) {
      setState('empty')
      return
    }
    await navigator.clipboard.writeText(shareLink(tab, text))
    setState('copied')
  }

  return (
    <button
      type="button"
      className={`icon-button share-link ${state} ${className}`.trim()}
      title={LABELS[state]}
      aria-label={LABELS[state]}
      onClick={() => void copy()}
    >
      {state === 'copied' ? (
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M5 12.5l4.5 4.5L19 7.5" />
        </svg>
      ) : (
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M10 13.5a4 4 0 0 0 5.66 0l3-3a4 4 0 0 0-5.66-5.66l-1.5 1.5" />
          <path d="M14 10.5a4 4 0 0 0-5.66 0l-3 3a4 4 0 0 0 5.66 5.66l1.5-1.5" />
        </svg>
      )}
    </button>
  )
}
