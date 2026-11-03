import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import type { Vocabulary } from '../../api'
import { expandTerm, splitCurie } from './terms'

type TermKind = 'properties' | 'classes'

export type TermInputProps = {
  /** The vocabularies the project pins, or null while they are still being read. */
  vocabularies: Vocabulary[] | null
  /** Which terms of a vocabulary to complete: its properties or its classes. */
  kind: TermKind
  /** The text in the box, owned by the caller so it can clear it after a commit. */
  value: string
  onChange: (value: string) => void
  /** A term chosen or typed, as its full IRI. */
  onCommit: (iri: string) => void
  placeholder: string
  id?: string
}

/** One row of the completion list: a term of a vocabulary, or a prefix to narrow to. */
type Suggestion =
  | { type: 'term'; label: string; iri: string; comment: string }
  | { type: 'prefix'; label: string; namespace: string; count: number }

const MAX_SUGGESTIONS = 40

/** What the pinned document says a term means, for the completion row: its comment, definition, or description. */
function commentOf(vocabulary: Vocabulary, name: string): string {
  const statements = vocabulary.statements[name] ?? []
  const definition = statements.find(
    (statement) => /comment|definition|description/i.test(statement.predicate) && !statement.iri,
  )
  return definition?.value ?? ''
}

/**
 * The suggestions for what was typed, as the SPARQL editor completes a
 * `prefix:name`: after a known prefix and a colon, that vocabulary's terms
 * whose name holds the rest, the ones starting with it first; before the
 * colon, the prefixes starting with it and then every vocabulary's terms
 * holding it, so a term can be found without knowing its vocabulary.
 */
function suggest(text: string, kind: TermKind, vocabularies: Vocabulary[]): Suggestion[] {
  const typed = text.trim()
  const curie = splitCurie(typed, vocabularies)
  // the names starting with what was typed come first, the shorter ones
  // ahead, so `na` offers name before naics and namedPosition
  const rank = (names: string[], needle: string) => {
    const lowered = needle.toLowerCase()
    const starts = names
      .filter((name) => name.toLowerCase().startsWith(lowered))
      .sort((a, b) => a.length - b.length || a.localeCompare(b))
    const holds = names.filter(
      (name) => !name.toLowerCase().startsWith(lowered) && name.toLowerCase().includes(lowered),
    )
    return [...starts, ...holds]
  }
  if (curie) {
    return rank(curie.vocabulary[kind], curie.local)
      .slice(0, MAX_SUGGESTIONS)
      .map((name) => ({
        type: 'term',
        label: `${curie.vocabulary.prefix}:${name}`,
        iri: curie.vocabulary.namespace + name,
        comment: commentOf(curie.vocabulary, name),
      }))
  }
  if (typed.includes(':')) return []
  const suggestions: Suggestion[] = vocabularies
    .filter(
      (vocabulary) => vocabulary[kind].length > 0 && vocabulary.prefix.toLowerCase().startsWith(typed.toLowerCase()),
    )
    .map((vocabulary) => ({
      type: 'prefix',
      label: `${vocabulary.prefix}:`,
      namespace: vocabulary.namespace,
      count: vocabulary[kind].length,
    }))
  if (typed.length >= 2) {
    for (const vocabulary of vocabularies) {
      for (const name of rank(vocabulary[kind], typed)) {
        if (suggestions.length >= MAX_SUGGESTIONS) break
        suggestions.push({
          type: 'term',
          label: `${vocabulary.prefix}:${name}`,
          iri: vocabulary.namespace + name,
          comment: commentOf(vocabulary, name),
        })
      }
    }
  }
  return suggestions.slice(0, MAX_SUGGESTIONS)
}

/**
 * A box for one RDF term that completes it the way the SPARQL editor
 * completes a `prefix:name`. Typing offers the prefixes of the pinned
 * vocabularies; once one is followed by a colon, the terms of that
 * vocabulary, which narrow as the name is typed, each with what its pinned
 * document says it means. Enter or a click takes a term, and commits it as
 * its full IRI; a full IRI typed in commits as it is. A bare word commits
 * nothing and says why.
 */
export function TermInput({ vocabularies, kind, value, onChange, onCommit, placeholder, id }: TermInputProps) {
  const [open, setOpen] = useState(false)
  const [selected, setSelected] = useState(0)
  const [problem, setProblem] = useState<string | null>(null)
  const input = useRef<HTMLInputElement>(null)
  const list = useRef<HTMLUListElement>(null)
  const known = useMemo(() => vocabularies ?? [], [vocabularies])
  const suggestions = useMemo(() => (open ? suggest(value, kind, known) : []), [open, value, kind, known])
  const index = Math.min(selected, Math.max(suggestions.length - 1, 0))

  useEffect(() => {
    list.current?.children[index]?.scrollIntoView({ block: 'nearest' })
  }, [index, suggestions])

  const choose = (suggestion: Suggestion) => {
    if (suggestion.type === 'prefix') {
      onChange(suggestion.label)
      setSelected(0)
      setOpen(true)
      input.current?.focus()
      return
    }
    setProblem(null)
    setOpen(false)
    onCommit(suggestion.iri)
  }

  const commitTyped = () => {
    const iri = expandTerm(value, known)
    if (!iri) {
      setProblem(value.trim() ? `${value.trim()} is neither a prefix:name of a pinned vocabulary nor a full IRI` : null)
      return
    }
    setProblem(null)
    setOpen(false)
    onCommit(iri)
  }

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      if (!open) setOpen(true)
      else setSelected(Math.min(index + 1, suggestions.length - 1))
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      setSelected(Math.max(index - 1, 0))
    } else if (event.key === 'Enter') {
      event.preventDefault()
      if (open && suggestions[index]) choose(suggestions[index])
      else commitTyped()
    } else if (event.key === 'Escape') {
      setOpen(false)
    } else if (event.key === 'Tab' && open && suggestions[index]?.type === 'prefix') {
      event.preventDefault()
      choose(suggestions[index])
    }
  }

  return (
    <div className="term-input">
      <div className="term-input-row">
        <div className="term-input-box">
          <input
            ref={input}
            id={id}
            className="module-input"
            value={value}
            placeholder={placeholder}
            spellCheck={false}
            autoComplete="off"
            role="combobox"
            aria-expanded={open && suggestions.length > 0}
            aria-autocomplete="list"
            onChange={(event) => {
              onChange(event.target.value)
              setSelected(0)
              setOpen(true)
              setProblem(null)
            }}
            onFocus={() => setOpen(true)}
            onBlur={() => setOpen(false)}
            onKeyDown={onKeyDown}
          />
          {open && suggestions.length > 0 && (
            <ul ref={list} className="term-suggestions" role="listbox">
              {suggestions.map((suggestion, i) => (
                <li
                  key={suggestion.type === 'term' ? suggestion.iri : suggestion.namespace}
                  role="option"
                  aria-selected={i === index}
                  className={i === index ? 'selected' : undefined}
                  // mousedown rather than click, so the input's blur does not close the list first
                  onMouseDown={(event) => {
                    event.preventDefault()
                    choose(suggestion)
                  }}
                  onMouseEnter={() => setSelected(i)}
                >
                  <code>{suggestion.label}</code>
                  {suggestion.type === 'prefix' ? (
                    <span className="term-suggestion-note">
                      {suggestion.count} {kind} · {suggestion.namespace}
                    </span>
                  ) : (
                    suggestion.comment && <span className="term-suggestion-note">{suggestion.comment}</span>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
      {problem && <p className="hint term-problem">{problem}</p>}
    </div>
  )
}
