import type Yasgui from '@zazuko/yasgui'
import { lookupTerm, type TermInfo } from './completers'

/*
 * Which of the project's vocabulary terms the editor is on. While the
 * completion list is open it is the highlighted suggestion, so the
 * Definitions pane (TermDefinitions.tsx) describes each candidate as the
 * list is walked; otherwise it is the prefixed name the caret sits on, or
 * the one selected. Every lookup reads the /api/vocabularies listing the
 * completers use; nothing is fetched as the caret moves.
 */

type Yasqe = InstanceType<typeof Yasgui.Yasqe>

type Position = { line: number; ch: number }
/**
 * The CodeMirror 5 surface this needs, named here since the app has no
 * typings for the editor Yasqe extends.
 */
type Editor = {
  getCursor(): Position
  somethingSelected(): boolean
  getSelection(): string
  getTokenAt(pos: Position): { string: string; start: number; end: number }
  getCompleteToken(token: unknown, pos: Position): { string: string; start: number; end: number }
  on(event: 'cursorActivity', handler: () => void): void
  off(event: 'cursorActivity', handler: () => void): void
}

/**
 * What show-hint's completion widget holds that this reads: the list the
 * hints came from (each a string or an object carrying its text), the index
 * of the highlighted one, and the <ul> the list is drawn in, whose <li>s
 * carry CodeMirror-hint-active as the highlight moves.
 */
type HintWidget = {
  data: { list: (string | { text: string })[] }
  selectedHint: number
  hints: HTMLElement
}

/**
 * The prefixed name selected in the editor, or the one the caret sits on or
 * just before, when the project's vocabularies know it. The tokenizer splits
 * a prefixed name at the colon, so the whole of it is read back with
 * getCompleteToken the way the completers do.
 */
export function termAtCaret(yasqe: Yasqe): TermInfo | null {
  const cm = yasqe as unknown as Editor
  if (cm.somethingSelected()) {
    const selected = lookupTerm(yasqe, cm.getSelection().trim())
    if (selected) return selected
  }
  const cursor = cm.getCursor()
  // getTokenAt answers the token ending at or after the position, so a caret
  // placed right before a name is not on it; the next character is tried
  // then, so the name is found from either end.
  for (const pos of [cursor, { line: cursor.line, ch: cursor.ch + 1 }]) {
    const token = cm.getCompleteToken(cm.getTokenAt(pos), pos)
    if (!token.string.includes(':')) continue
    const term = lookupTerm(yasqe, token.string)
    if (term) return term
  }
  return null
}

/** The term the completion widget has highlighted, when it is one of the project's. */
function highlightedTerm(yasqe: Yasqe, widget: HintWidget): TermInfo | null {
  const active = widget.hints.querySelector('.CodeMirror-hint-active')
  const index = active ? Array.from(widget.hints.children).indexOf(active) : widget.selectedHint
  const hint = widget.data.list[index]
  if (hint === undefined) return null
  return lookupTerm(yasqe, typeof hint === 'string' ? hint : hint.text)
}

/**
 * Calls onChange with the term the editor is on now and whenever that
 * changes: as the caret moves or the query changes (CodeMirror fires
 * cursorActivity for both), and while the completion list is open as the
 * highlight moves through it, which show-hint marks on the list's <li>s and
 * is read back through a MutationObserver since the widget's own select
 * event is signalled on an object this has no handle on. Returns the
 * function that stops watching.
 */
export function watchCaretTerm(yasqe: Yasqe, onChange: (term: TermInfo | null) => void): () => void {
  const cm = yasqe as unknown as Editor
  let widget: HintWidget | null = null
  const highlight = new MutationObserver(() => {
    if (widget) onChange(highlightedTerm(yasqe, widget))
  })
  const update = () => onChange(widget ? highlightedTerm(yasqe, widget) : termAtCaret(yasqe))
  const shown = (_yasqe: Yasqe, opened: HintWidget) => {
    widget = opened
    highlight.observe(opened.hints, { attributes: true, attributeFilter: ['class'], subtree: true })
    update()
  }
  const closed = () => {
    widget = null
    highlight.disconnect()
    update()
  }
  cm.on('cursorActivity', update)
  yasqe.on('autocompletionShown', shown)
  yasqe.on('autocompletionClose', closed)
  update()
  return () => {
    cm.off('cursorActivity', update)
    yasqe.off('autocompletionShown', shown)
    yasqe.off('autocompletionClose', closed)
    highlight.disconnect()
  }
}
