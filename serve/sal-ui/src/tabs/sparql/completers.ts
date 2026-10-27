import type Yasgui from '@zazuko/yasgui'
import type { Vocabulary } from '../../api'

/*
 * Autocompletion from the project's own vocabularies. YASQE ships completers
 * that ask lov.linkeddata.es for classes and properties and prefix.cc for
 * prefixes on every keystroke: slow, offline-blind, and ignorant of what this
 * project pinned, a salmodule:// ontology above all. These two replace them
 * with lookups over what /api/vocabularies lists, the terms of the versions
 * a build validated against, so a suggestion is always a term the table can
 * hold. Both read the module-level store below at call time, so they work
 * from the moment the listing arrives, whichever tab was built first.
 */

type Yasqe = InstanceType<typeof Yasgui.Yasqe>
type CompleterConfig = Parameters<typeof Yasgui.Yasqe.registerAutocompleter>[0]
type Hint = ReturnType<NonNullable<CompleterConfig['postprocessHints']>>[number]
/** The token YASQE hands a completer, with what the prefix completer's own listener fills in. */
type CompletionToken = { string: string; autocompletionString?: string }
/**
 * What getCompleteToken returns. Its declared type is CodeMirror's, which the
 * app has no typings for, so the fields read here are named explicitly.
 */
type CompleteToken = {
  string: string
  state?: { possibleCurrent?: string[]; lastProperty?: string }
}

const completeToken = (yasqe: Yasqe) => yasqe.getCompleteToken() as unknown as CompleteToken
const previousToken = (yasqe: Yasqe, token: CompleteToken) =>
  yasqe.getPreviousNonWsToken(yasqe.getDoc().getCursor().line, token as never) as unknown as CompleteToken | undefined

export type TermKind = 'class' | 'property' | 'other'

type Term = { name: string; kind: TermKind }

/** A vocabulary with its terms merged into one sorted list, for looking up by namespace. */
type VocabularyIndex = { vocabulary: Vocabulary; terms: Term[]; kinds: Map<string, TermKind> }

/** The most suggestions a hint list shows, the same bound YASQE's own completers use. */
const SUGGESTIONS_LIMIT = 100

/** The name the editor itself gives its completers, enabled on every tab. */
const PREFIX_COMPLETER = 'salPrefixes'
const TERM_COMPLETER = 'salTerms'

let vocabularies: Vocabulary[] = []
const byNamespace = new Map<string, VocabularyIndex>()

/**
 * Replaces the vocabularies the completers and the Prefixes dialog offer.
 * Two vocabularies the server names alike (http and https schema.org, say)
 * are told apart here by numbering the later one, so that the dialog and the
 * completers agree on what each is called.
 */
export function setVocabularies(list: Vocabulary[]) {
  const taken = new Set<string>()
  vocabularies = list.map((vocabulary) => {
    let prefix = vocabulary.prefix || 'ns'
    for (let n = 2; taken.has(prefix); n++) prefix = `${vocabulary.prefix || 'ns'}${n}`
    taken.add(prefix)
    return { ...vocabulary, prefix }
  })
  byNamespace.clear()
  for (const vocabulary of vocabularies) {
    const kinds = new Map<string, TermKind>()
    for (const name of vocabulary.classes) kinds.set(name, 'class')
    for (const name of vocabulary.properties) kinds.set(name, 'property')
    for (const name of vocabulary.others) kinds.set(name, 'other')
    const terms = Array.from(kinds, ([name, kind]) => ({ name, kind })).sort((a, b) => a.name.localeCompare(b.name))
    byNamespace.set(vocabulary.namespace, { vocabulary, terms, kinds })
  }
}

/** The vocabularies as setVocabularies left them, with their prefixes made unique. */
export function getVocabularies(): Vocabulary[] {
  return vocabularies
}

/** A prefixed name as typed: the prefix, the colon, and whatever local name follows. */
const PREFIXED_NAME = /^([A-Za-z_][\w.-]*):([^\s<>"'()[\]{},;|/^]*)$/

/**
 * What the token under the cursor is, when it is a prefixed name whose prefix
 * the query binds to one of the project's vocabularies; null otherwise, which
 * is also the answer on a PREFIX line, where the name is being declared rather
 * than used.
 */
function prefixedToken(yasqe: Yasqe): { prefix: string; local: string; index: VocabularyIndex } | null {
  const token = completeToken(yasqe)
  const match = PREFIXED_NAME.exec(token.string)
  if (!match) return null
  if (previousToken(yasqe, token)?.string?.toUpperCase() === 'PREFIX') return null
  const namespace = yasqe.getPrefixesFromQuery()[match[1]]
  if (!namespace) return null
  const index = byNamespace.get(namespace)
  if (!index) return null
  return { prefix: match[1], local: match[2], index }
}

/**
 * Where in a triple the token sits, read off the tokenizer's state the way
 * YASQE's own class and property completers do: a predicate position can take
 * `a`, and an object after `a` or rdf:type is a class.
 */
function position(yasqe: Yasqe): 'predicate' | 'class' | 'other' {
  const token = completeToken(yasqe)
  const last = previousToken(yasqe, token)?.state?.lastProperty
  if (last === 'a' || last === 'rdf:type') return 'class'
  if (token.state?.possibleCurrent?.includes('a')) return 'predicate'
  return 'other'
}

/**
 * The terms of a vocabulary ranked against what was typed: a term the typed
 * text starts, case first, then one it starts ignoring case, then one it
 * appears inside; within a tier, the kind the position calls for first, and
 * then alphabetically.
 */
function rankTerms(terms: Term[], typed: string, at: 'predicate' | 'class' | 'other'): Term[] {
  const lower = typed.toLowerCase()
  const wanted: TermKind | null = at === 'predicate' ? 'property' : at === 'class' ? 'class' : null
  const scored: { term: Term; score: number }[] = []
  for (const term of terms) {
    let tier: number
    if (typed === '' || term.name.startsWith(typed)) tier = 0
    else if (term.name.toLowerCase().startsWith(lower)) tier = 1
    else if (term.name.toLowerCase().includes(lower)) tier = 2
    else continue
    const kindTier = wanted === null || term.kind === wanted ? 0 : term.kind === 'other' ? 2 : 1
    scored.push({ term, score: tier * 10 + kindTier })
  }
  return scored
    .sort((a, b) => a.score - b.score || a.term.name.localeCompare(b.term.name))
    .slice(0, SUGGESTIONS_LIMIT)
    .map((entry) => entry.term)
}

/** The kind of the term a hint inserts, for the tag beside it; null when it is not one of ours. */
function hintKind(yasqe: Yasqe, text: string): TermKind | null {
  const colon = text.indexOf(':')
  if (colon < 0) return null
  const namespace = yasqe.getPrefixesFromQuery()[text.slice(0, colon)]
  if (!namespace) return null
  return byNamespace.get(namespace)?.kinds.get(text.slice(colon + 1)) ?? null
}

/**
 * Draws a hint as the prefixed name with its kind tagged at the right.
 * CodeMirror's show-hint calls a renderer with the whole hint list second and
 * the hint itself third, whatever YASQE's typing says.
 */
function renderHint(this: Yasqe, element: HTMLElement, _list: unknown, hint: Hint) {
  element.append(hint.displayText ?? hint.text)
  const kind = hintKind(this, hint.text)
  if (kind && kind !== 'other') {
    const tag = document.createElement('span')
    tag.className = 'sal-hint-kind'
    tag.textContent = kind
    element.append(tag)
  }
}

let registered = false

/**
 * Registers the two completers with YASQE and makes them, with the variable
 * completer, the only ones a new editor enables, in place of the three that
 * call out to public registries. Called once, before the first Yasgui is
 * built; a second call does nothing.
 */
export function registerSalCompleters(yasgui: typeof Yasgui) {
  if (registered) return
  registered = true
  const Yasqe = yasgui.Yasqe

  // A fork of the prefix completer keeps its listener that declares a prefix
  // the moment one is typed in a query, and its PREFIX-line position check;
  // only where the declarations come from changes. It is no longer a bulk
  // completer, so nothing is cached in localStorage across visits.
  Yasqe.forkAutocompleter(
    'prefixes',
    {
      name: PREFIX_COMPLETER,
      bulk: false,
      autoShow: true,
      persistenceId: undefined,
      get: (_yasqe: Yasqe, token?: CompletionToken) => {
        const needle = (token?.autocompletionString ?? token?.string ?? '').trim()
        return vocabularies
          .map((vocabulary) => `${vocabulary.prefix}: <${vocabulary.namespace}>`)
          .filter((declaration) => declaration.startsWith(needle))
      },
    } as Partial<CompleterConfig> & { name: string },
    false,
  )

  Yasqe.registerAutocompleter(
    {
      name: TERM_COMPLETER,
      bulk: false,
      autoShow: true,
      isValidCompletionPosition: (yasqe: Yasqe) => prefixedToken(yasqe) !== null,
      get: (yasqe: Yasqe) => {
        const found = prefixedToken(yasqe)
        if (!found) return []
        return rankTerms(found.index.terms, found.local, position(yasqe)).map((term) => `${found.prefix}:${term.name}`)
      },
      postprocessHints: (yasqe: Yasqe, hints: Hint[]) => {
        // A name already typed in full is nothing to suggest.
        const typed = completeToken(yasqe).string
        return hints
          .filter((hint) => hint.text !== typed)
          .map((hint) => ({ ...hint, render: renderHint.bind(yasqe) as unknown as Hint['render'] }))
      },
    },
    false,
  )

  // Yasqe merges its defaults into each editor's config with lodash's merge,
  // which merges arrays index by index, so the list is replaced in place
  // rather than handed over as a config option.
  Yasqe.defaults.autocompleters.splice(0, Yasqe.defaults.autocompleters.length, 'variables', PREFIX_COMPLETER, TERM_COMPLETER)
}
