import { useEffect, useState } from 'react'
import { shareLink, type TabName } from '../routing'

type ShareLinkButtonProps = {
  tab: TabName
  /** Read lazily, so the link always carries whatever the editor holds right now. */
  query: () => string
  className?: string
}

/**
 * Copies a link to this tab with the current query URL encoded into it. Opening
 * the link loads that query back into the editor, which is how a query is
 * handed to someone else without pasting the text itself.
 */
export function ShareLinkButton({ tab, query, className = 'button' }: ShareLinkButtonProps) {
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
      className={state === 'copied' ? `${className} copied` : className}
      title="Copy a link that reopens this tab with the current query"
      onClick={() => void copy()}
    >
      {state === 'copied' ? 'Link copied' : state === 'empty' ? 'Nothing to share' : 'Copy link'}
    </button>
  )
}
