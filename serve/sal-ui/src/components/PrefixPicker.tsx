import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { createPortal } from 'react-dom'
import type { Vocabulary } from '../api'

/** The prefix declarations a query holds, as YASQE reads them: prefix label to namespace. */
export type Prefixes = Record<string, string>

type PrefixPickerProps = {
  /** The vocabularies the project pins, or null while they are still being read. */
  vocabularies: Vocabulary[] | null
  error: string | null
  /** The prefixes the active query declares, read when the dialog opens and after every insertion. */
  declared: () => Prefixes
  /** Adds PREFIX lines for the given prefixes to the active query. */
  onInsert: (prefixes: Prefixes) => void
  /** The small button, for a panel header strip rather than a toolbar. */
  compact?: boolean
}

const formatCount = (value: number) => new Intl.NumberFormat().format(value)

/** How many terms the vocabulary's pinned document defines under its namespace. */
function termCount(vocabulary: Vocabulary) {
  return vocabulary.classes.length + vocabulary.properties.length + vocabulary.others.length
}

/** The label the query already binds to namespace, if any. */
function declaredAs(declared: Prefixes, namespace: string): string | null {
  for (const [label, bound] of Object.entries(declared)) {
    if (bound === namespace) return label
  }
  return null
}

/**
 * The label to declare a vocabulary under: its own prefix, unless the query
 * already binds that label to something else, in which case a numbered one.
 */
function freeLabel(declared: Prefixes, vocabulary: Vocabulary): string {
  let label = vocabulary.prefix
  for (let n = 2; label in declared && declared[label] !== vocabulary.namespace; n++) label = `${vocabulary.prefix}${n}`
  return label
}

/**
 * The **Prefixes** button of the SPARQL editor and the dialog it opens: every
 * vocabulary the project pins, the same ones the Stats tab lists, each with
 * the prefix it is known by and how many terms its pinned document defines.
 * Choosing one adds its PREFIX line to the active query, after which typing
 * the prefix and a colon completes its terms; **Add all** declares every
 * vocabulary not yet declared. A vocabulary the query already declares, under
 * whatever label, is marked rather than added twice.
 */
export function PrefixPicker({ vocabularies, error, declared, onInsert, compact = false }: PrefixPickerProps) {
  const [open, setOpen] = useState(false)
  const [search, setSearch] = useState('')
  const [selected, setSelected] = useState(0)
  // What the query declares, read when the dialog opens and after every
  // insertion, so the marks follow the editor.
  const [current, setCurrent] = useState<Prefixes>({})
  const input = useRef<HTMLInputElement>(null)
  const list = useRef<HTMLUListElement>(null)
  const titleId = useId()

  const matches = useMemo(() => {
    const terms = search.toLowerCase().split(/\s+/).filter(Boolean)
    return (vocabularies ?? []).filter((vocabulary) =>
      terms.every((term) => vocabulary.prefix.toLowerCase().includes(term) || vocabulary.namespace.toLowerCase().includes(term)),
    )
  }, [vocabularies, search])

  useEffect(() => {
    if (!open) return
    setSearch('')
    setSelected(0)
    setCurrent(declared())
    input.current?.focus()
  }, [open, declared])

  const index = Math.min(selected, Math.max(matches.length - 1, 0))

  useEffect(() => {
    list.current?.children[index]?.scrollIntoView({ block: 'nearest' })
  }, [index, matches])

  const insert = (chosen: Vocabulary[]) => {
    const prefixes: Prefixes = {}
    const bound = { ...current }
    for (const vocabulary of chosen) {
      if (declaredAs(bound, vocabulary.namespace)) continue
      const label = freeLabel(bound, vocabulary)
      prefixes[label] = vocabulary.namespace
      bound[label] = vocabulary.namespace
    }
    if (Object.keys(prefixes).length > 0) onInsert(prefixes)
    setCurrent(declared())
  }

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Escape') {
      event.preventDefault()
      setOpen(false)
    } else if (event.key === 'ArrowDown') {
      event.preventDefault()
      setSelected(Math.min(index + 1, matches.length - 1))
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      setSelected(Math.max(index - 1, 0))
    } else if (event.key === 'Enter') {
      event.preventDefault()
      if (matches[index]) insert([matches[index]])
    }
  }

  const undeclared = (vocabularies ?? []).filter((vocabulary) => !declaredAs(current, vocabulary.namespace))

  return (
    <>
      <button
        type="button"
        className={`button ${compact ? 'small' : ''}`.trim()}
        aria-haspopup="dialog"
        aria-expanded={open}
        title="Declare the prefixes of the vocabularies this project pins"
        onClick={() => setOpen(true)}
      >
        Prefixes
      </button>
      {open &&
        createPortal(
          <div className="modal-backdrop" onMouseDown={() => setOpen(false)}>
            <div
              className="modal prefix-picker"
              role="dialog"
              aria-modal="true"
              aria-labelledby={titleId}
              onMouseDown={(event) => event.stopPropagation()}
              onKeyDown={onKeyDown}
            >
              <div className="modal-header">
                <h3 id={titleId}>Project prefixes</h3>
                <button type="button" className="icon-button" aria-label="Close" onClick={() => setOpen(false)}>
                  ×
                </button>
              </div>
              <input
                ref={input}
                className="module-input modal-search"
                type="search"
                placeholder={vocabularies ? `Search ${vocabularies.length} vocabularies…` : 'Reading the pinned vocabularies…'}
                value={search}
                onChange={(event) => {
                  setSearch(event.target.value)
                  setSelected(0)
                }}
                aria-controls={`${titleId}-list`}
                aria-activedescendant={matches[index] ? `${titleId}-${index}` : undefined}
              />
              {error && <p className="error-banner modal-banner">{error}</p>}
              <ul id={`${titleId}-list`} ref={list} className="sample-list prefix-list" role="listbox">
                {matches.map((vocabulary, i) => {
                  const label = declaredAs(current, vocabulary.namespace)
                  return (
                    <li
                      key={vocabulary.id}
                      id={`${titleId}-${i}`}
                      role="option"
                      aria-selected={i === index}
                      className={`prefix-item${i === index ? ' selected' : ''}${label ? ' declared' : ''}`}
                      title={label ? `Declared in the query as ${label}:` : `Add PREFIX ${vocabulary.prefix}: <${vocabulary.namespace}>`}
                      onMouseEnter={() => setSelected(i)}
                      onClick={() => insert([vocabulary])}
                    >
                      <code className="prefix-name">{label ?? vocabulary.prefix}:</code>
                      <span className="prefix-namespace">{vocabulary.namespace}</span>
                      <span className="prefix-meta">
                        {vocabulary.error ? (
                          <span className="prefix-unavailable" title={vocabulary.error}>
                            terms unavailable
                          </span>
                        ) : (
                          <span>{formatCount(termCount(vocabulary))} terms</span>
                        )}
                        {vocabulary.imported && <span className="badge prefix-badge">imported</span>}
                        {label && (
                          <span className="prefix-declared" aria-label="Declared">
                            ✓
                          </span>
                        )}
                      </span>
                    </li>
                  )
                })}
                {vocabularies && vocabularies.length === 0 && !error && (
                  <li className="sample-empty">
                    The project pins no vocabularies yet. Run <code>sal build</code> or <code>sal validate</code> to pin the
                    ones it resolves against; a remote table lists none.
                  </li>
                )}
                {vocabularies && vocabularies.length > 0 && matches.length === 0 && (
                  <li className="sample-empty">Nothing matches “{search}”.</li>
                )}
              </ul>
              <div className="modal-actions">
                <p className="hint">
                  <kbd>↑</kbd> <kbd>↓</kbd> to choose, <kbd>Enter</kbd> to add, <kbd>Esc</kbd> to close. In the editor, a
                  declared prefix and a colon complete the vocabulary&apos;s terms.
                </p>
                <button
                  type="button"
                  className="button small"
                  disabled={undeclared.length === 0}
                  title="Declare every vocabulary the query does not yet"
                  onClick={() => insert(undeclared)}
                >
                  Add all
                </button>
              </div>
            </div>
          </div>,
          document.body,
        )}
    </>
  )
}
