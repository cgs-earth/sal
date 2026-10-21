import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { createPortal } from 'react-dom'

/** A starter query the picker offers: its name, and the SQL or SPARQL it loads. */
export type SampleQuery = { name: string; text: string }

type SampleQueryPickerProps = {
  samples: SampleQuery[]
  /** Called with the chosen sample; the picker closes itself. */
  onPick: (sample: SampleQuery) => void
  /** The small button, for a panel header strip rather than a toolbar. */
  compact?: boolean
}

type Match = { sample: SampleQuery; score: number; nameIndices: number[] }

/**
 * Scores how well `needle` matches `haystack` as a subsequence: every character
 * of the needle has to appear in order, and a character that starts a word or
 * follows the previous match scores higher than one found further on. Returns
 * null when the needle is not a subsequence at all. The indices are where in
 * the haystack each needle character landed, for highlighting.
 */
function fuzzyScore(needle: string, haystack: string): { score: number; indices: number[] } | null {
  const lower = haystack.toLowerCase()
  // A term found whole outranks any scattered match of its letters, and is
  // highlighted as the run it is rather than as the first letters that fit.
  const whole = lower.indexOf(needle)
  if (whole !== -1) {
    return { score: needle.length * 4, indices: Array.from(needle, (_, offset) => whole + offset) }
  }
  const indices: number[] = []
  let score = 0
  let from = 0
  for (const char of needle) {
    const at = lower.indexOf(char, from)
    if (at === -1) return null
    const previous = indices[indices.length - 1]
    if (previous !== undefined && at === previous + 1) score += 3
    else if (at === 0 || /[\s_:(?.]/.test(haystack[at - 1])) score += 2
    else score += 1
    indices.push(at)
    from = at + 1
  }
  // A match packed into fewer characters is the better one.
  return { score: score - (indices[indices.length - 1] - indices[0]) / 50, indices }
}

/**
 * Ranks the samples against what was typed. The name is what is searched
 * first; a term that is not in the name may still match the query text, so a
 * search for a predicate or a keyword finds the samples using it, ranked below
 * the ones named for it. Spaces separate terms that must all match somewhere.
 */
function rank(samples: SampleQuery[], search: string): Match[] {
  const terms = search.toLowerCase().split(/\s+/).filter(Boolean)
  if (terms.length === 0) return samples.map((sample) => ({ sample, score: 0, nameIndices: [] }))
  const matches: Match[] = []
  for (const sample of samples) {
    let score = 0
    let nameIndices: number[] = []
    let matched = true
    for (const term of terms) {
      const inName = fuzzyScore(term, sample.name)
      if (inName) {
        score += inName.score + 10
        nameIndices = nameIndices.concat(inName.indices)
        continue
      }
      const inText = fuzzyScore(term, sample.text)
      if (!inText) {
        matched = false
        break
      }
      score += inText.score
    }
    if (matched) matches.push({ sample, score, nameIndices })
  }
  return matches.sort((a, b) => b.score - a.score)
}

/** The sample's name with the matched characters marked. */
function highlighted(name: string, indices: number[]) {
  if (indices.length === 0) return name
  const marks = new Set(indices)
  return Array.from(name, (char, index) =>
    marks.has(index) ? (
      <mark key={index}>{char}</mark>
    ) : (
      <span key={index}>{char}</span>
    ),
  )
}

/** The first lines of a query, for the preview under its name. */
function preview(text: string): string {
  const lines = text
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line && !/^PREFIX\s/i.test(line))
  return lines.slice(0, 3).join(' ⏎ ')
}

/**
 * The **Examples** button of an editor and the dialog it opens: a search box
 * over the starter queries, matched fuzzily against their names and text, with
 * the matches listed under it. Enter or a click loads the highlighted one into
 * the editor; Escape or a click outside closes the dialog. It replaces a row of
 * one chip per sample, which grew with every sample the server added.
 */
export function SampleQueryPicker({ samples, onPick, compact = false }: SampleQueryPickerProps) {
  const [open, setOpen] = useState(false)
  const [search, setSearch] = useState('')
  const [selected, setSelected] = useState(0)
  const input = useRef<HTMLInputElement>(null)
  const list = useRef<HTMLUListElement>(null)
  const titleId = useId()

  const matches = useMemo(() => rank(samples, search), [samples, search])

  useEffect(() => {
    if (!open) return
    setSearch('')
    setSelected(0)
    input.current?.focus()
  }, [open])

  // The selection follows the list: a search that leaves fewer matches than the
  // index pointed at moves it back onto the last one.
  const current = Math.min(selected, Math.max(matches.length - 1, 0))

  useEffect(() => {
    list.current?.children[current]?.scrollIntoView({ block: 'nearest' })
  }, [current, matches])

  const pick = (match: Match | undefined) => {
    if (!match) return
    onPick(match.sample)
    setOpen(false)
  }

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Escape') {
      event.preventDefault()
      setOpen(false)
    } else if (event.key === 'ArrowDown') {
      event.preventDefault()
      setSelected(Math.min(current + 1, matches.length - 1))
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      setSelected(Math.max(current - 1, 0))
    } else if (event.key === 'Enter') {
      event.preventDefault()
      pick(matches[current])
    }
  }

  return (
    <>
      <button
        type="button"
        className={`button ${compact ? 'small' : ''}`.trim()}
        aria-haspopup="dialog"
        aria-expanded={open}
        title="Search the example queries"
        onClick={() => setOpen(true)}
      >
        Examples
      </button>
      {open &&
        createPortal(
          <div className="modal-backdrop" onMouseDown={() => setOpen(false)}>
            <div
              className="modal sample-picker"
              role="dialog"
              aria-modal="true"
              aria-labelledby={titleId}
              onMouseDown={(event) => event.stopPropagation()}
              onKeyDown={onKeyDown}
            >
              <div className="modal-header">
                <h3 id={titleId}>Example queries</h3>
                <button type="button" className="icon-button" aria-label="Close" onClick={() => setOpen(false)}>
                  ×
                </button>
              </div>
              <input
                ref={input}
                className="module-input modal-search"
                type="search"
                placeholder={`Search ${samples.length} examples…`}
                value={search}
                onChange={(event) => {
                  setSearch(event.target.value)
                  setSelected(0)
                }}
                aria-controls={`${titleId}-list`}
                aria-activedescendant={matches[current] ? `${titleId}-${current}` : undefined}
              />
              <ul id={`${titleId}-list`} ref={list} className="sample-list" role="listbox">
                {matches.map((match, index) => (
                  <li
                    key={match.sample.name}
                    id={`${titleId}-${index}`}
                    role="option"
                    aria-selected={index === current}
                    className={index === current ? 'sample-item selected' : 'sample-item'}
                    onMouseEnter={() => setSelected(index)}
                    onClick={() => pick(match)}
                  >
                    <span className="sample-name">{highlighted(match.sample.name, match.nameIndices)}</span>
                    <code className="sample-preview">{preview(match.sample.text)}</code>
                  </li>
                ))}
                {matches.length === 0 && <li className="sample-empty">Nothing matches “{search}”.</li>}
              </ul>
              <p className="hint modal-hint">
                <kbd>↑</kbd> <kbd>↓</kbd> to choose, <kbd>Enter</kbd> to load, <kbd>Esc</kbd> to close
              </p>
            </div>
          </div>,
          document.body,
        )}
    </>
  )
}
