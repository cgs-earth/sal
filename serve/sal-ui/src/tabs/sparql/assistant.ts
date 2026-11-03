import { useSyncExternalStore } from 'react'
import { OpenRouter, stepCountIs, tool, type ConversationState, type StateAccessor } from '@openrouter/agent'
import { z } from 'zod'
import { runSparql, type SparqlResults, type TermStatement, type Vocabulary } from '../../api'

/*
 * The SPARQL assistant: a chat in the SPARQL tab that writes queries for the
 * dataset the server holds, run entirely in the browser against OpenRouter
 * with a key the user pastes in. The model is given the project's pinned
 * vocabularies as context and a few tools over this server's own endpoints,
 * so it can look a term up, see which classes and predicates the table
 * actually uses, and run a draft to read the translator's error or the rows
 * it answers before handing the query over. Nothing here goes through the Go
 * server: the key and the conversation go to openrouter.ai and nowhere else,
 * and the tools call the same /sparql the editor does.
 */

/** Where the key and the model are remembered between visits. */
const API_KEY_STORAGE = 'sal-ui.openrouter.apiKey'
const MODEL_STORAGE = 'sal-ui.openrouter.model'

/** The model used until another is chosen. */
export const DEFAULT_MODEL = 'anthropic/claude-sonnet-5.5'

/** Where the settings form can read the models OpenRouter serves; no key is needed. */
export const MODELS_URL = 'https://openrouter.ai/api/v1/models'

export type AssistantSettings = {
  apiKey: string
  model: string
}

export function readSettings(): AssistantSettings {
  try {
    return {
      apiKey: localStorage.getItem(API_KEY_STORAGE) ?? '',
      model: localStorage.getItem(MODEL_STORAGE) || DEFAULT_MODEL,
    }
  } catch {
    return { apiKey: '', model: DEFAULT_MODEL }
  }
}

/** Remembers the settings in this browser; an empty key forgets the one stored. */
export function writeSettings(settings: AssistantSettings) {
  try {
    if (settings.apiKey) localStorage.setItem(API_KEY_STORAGE, settings.apiKey)
    else localStorage.removeItem(API_KEY_STORAGE)
    localStorage.setItem(MODEL_STORAGE, settings.model || DEFAULT_MODEL)
  } catch {
    // Storage may be unavailable; the settings still hold for this visit through the caller's state.
  }
}

/* ---------- the conversation as the pane shows it ---------- */

/** A run of the assistant's prose, rendered as light markdown. */
export type TextPart = { kind: 'text'; text: string }

/** One tool call the assistant made, shown as a row the user can open. */
export type ToolPart = {
  kind: 'tool'
  id: number
  /** What was done, in a few words: "Ran a query", "Looked up schema:name". */
  title: string
  /** What came back, in a line: "12 rows", or the error the tool answered. */
  summary: string
  /** What the model asked for and what it got, for the row's details. */
  input: unknown
  output?: unknown
  status: 'running' | 'done' | 'failed'
}

export type AssistantPart = TextPart | ToolPart

export type ChatMessage =
  | { id: number; role: 'user'; text: string }
  | {
      id: number
      role: 'assistant'
      parts: AssistantPart[]
      status: 'streaming' | 'done' | 'stopped' | 'failed'
      /** Why the turn failed, when it did. */
      error?: string
    }

export type Chat = {
  messages: ChatMessage[]
  /** Whether a turn is in flight. */
  busy: boolean
}

/*
 * A module-level store rather than state in the pane, so a conversation
 * survives the pane being closed and reopened and the tab being left and
 * returned to, the way the editor's own tabs do. One conversation at a time.
 */
let chat: Chat = { messages: [], busy: false }
const listeners = new Set<() => void>()
/** The history the agent loop carries between turns, in the SDK's own shape. */
let conversation: ConversationState | null = null
/** The editor's query as it was last described to the model, so it is only repeated when it changed. */
let describedQuery = ''
let controller: AbortController | null = null
let nextId = 1

function publish(next: Chat) {
  chat = next
  for (const listener of listeners) listener()
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

export function useChat(): Chat {
  return useSyncExternalStore(subscribe, () => chat)
}

/** Forgets the conversation, stopping a turn in flight. */
export function resetChat() {
  controller?.abort()
  controller = null
  conversation = null
  describedQuery = ''
  publish({ messages: [], busy: false })
}

/** Stops the turn in flight, keeping what the assistant has said so far. */
export function stopAssistant() {
  controller?.abort()
}

/** Replaces the assistant message with the given id. */
function updateAssistant(id: number, change: (message: ChatMessage & { role: 'assistant' }) => ChatMessage) {
  publish({
    ...chat,
    messages: chat.messages.map((message) => (message.id === id && message.role === 'assistant' ? change(message) : message)),
  })
}

/* ---------- what the model is told ---------- */

/** How many terms a lookup lists at most, and how many rows a query hands back. */
const MAX_TERMS = 60
const MAX_ROWS = 25
const MAX_VALUE_LENGTH = 300
/** The LIMIT added to a draft query that states none, so a draft never pulls the whole table. */
const DRAFT_LIMIT = 25
/** How many tool rounds a turn may take before the model is asked to answer with what it has. */
const MAX_STEPS = 12

/** A statement's predicate without its prefix, for picking a label or a comment out of a term's statements. */
function localName(predicate: string): string {
  return predicate.slice(predicate.search(/[:#/][^:#/]*$/) + 1)
}

/** The first comment, definition, or description a term's statements carry, cut to a line. */
function commentOf(statements: TermStatement[] | undefined): string | undefined {
  const found = statements?.find((statement) => /^(comment|definition|description|note)$/i.test(localName(statement.predicate)))
  if (!found) return undefined
  return found.value.length > 200 ? `${found.value.slice(0, 200)}…` : found.value
}

type Term = { name: string; kind: 'class' | 'property' | 'other' }

function termsOf(vocabulary: Vocabulary): Term[] {
  return [
    ...vocabulary.classes.map((name): Term => ({ name, kind: 'class' })),
    ...vocabulary.properties.map((name): Term => ({ name, kind: 'property' })),
    ...vocabulary.others.map((name): Term => ({ name, kind: 'other' })),
  ]
}

/** The vocabulary a prefix names, matched ignoring case. */
function vocabularyFor(vocabularies: Vocabulary[], prefix: string): Vocabulary | undefined {
  const wanted = prefix.replace(/:$/, '').toLowerCase()
  return vocabularies.find((vocabulary) => vocabulary.prefix.toLowerCase() === wanted)
}

/** A prefixed name expanded against the pinned vocabularies; a full IRI, with or without angle brackets, is kept. */
function expandTerm(vocabularies: Vocabulary[], term: string): string {
  const trimmed = term.trim().replace(/^<|>$/g, '')
  const match = /^([A-Za-z_][\w.-]*):(.*)$/.exec(trimmed)
  if (!match || /^https?$/i.test(match[1])) return trimmed
  const vocabulary = vocabularyFor(vocabularies, match[1])
  return vocabulary ? vocabulary.namespace + match[2] : trimmed
}

/** The instructions the model works under for the given project. */
function instructions(vocabularies: Vocabulary[], snapshots: string[]): string {
  const listing =
    vocabularies.length === 0
      ? 'The project pins no vocabularies, so nothing is known about its terms beyond what the table itself holds; use schema_overview and run_sparql to find out.'
      : vocabularies
          .map((vocabulary) => {
            const counts = `${vocabulary.classes.length} classes, ${vocabulary.properties.length} properties`
            const imported = vocabulary.imported ? ', imported into the table' : ''
            return `- PREFIX ${vocabulary.prefix}: <${vocabulary.namespace}> (${counts}${imported})`
          })
          .join('\n')
  const snapshotNote =
    snapshots.length > 1
      ? `The table's snapshot IDs, newest first: ${snapshots.join(', ')}. A SERVICE <${window.location.origin}/v{snapshot}/sparql> { ... } block reads the table at that snapshot, and MINUS between the two diffs them.`
      : 'The table has a single snapshot, so there is nothing to diff.'
  return `You are the SPARQL assistant of a SAL data product: an RDF graph built into one Iceberg triples table, queried through an endpoint that translates SPARQL to DuckDB SQL. You write SPARQL queries for the dataset the user asks about, and you explain, fix, or extend the query they have in the editor.

Work like this:
1. Start from what the dataset holds. Call schema_overview to see the classes and predicates the table actually uses; the pinned vocabularies describe what the project validated against, not what it asserted, and a term that is defined but unused answers nothing.
2. Look up a term you are not sure of with find_terms or describe_term rather than guessing a name or a domain.
3. Run every draft with run_sparql before answering. Read the error if the translator rejects it, or the rows if it runs, and revise until it answers the question. Explain in a sentence when a query answers no rows and why.
4. Answer briefly. Put the final query in exactly one fenced code block tagged sparql, complete with the PREFIX declarations it needs, after a sentence or two saying what it does. The user can insert that block into the editor and run it. Don't repeat intermediate drafts.

The vocabularies the project pins, with the prefixes to declare them under:
${listing}

Only a vocabulary marked imported has its own statements in the table, so a property path such as rdfs:subClassOf* walks only what the project asserted or imported.

${snapshotNote}

What the endpoint supports: SELECT, CONSTRUCT, and ASK (not DESCRIBE); triple patterns with prefixed names, full IRIs, the a keyword, and predicate-object lists; FILTER with comparisons, boolean operators, arithmetic, and the SPARQL builtins (STR, LANG, langMatches, DATATYPE, BOUND, isIRI, isLiteral, isNumeric, REGEX, CONTAINS, STRSTARTS, STRENDS, STRLEN, UCASE, LCASE, SUBSTR, CONCAT, COALESCE, IF, YEAR, MONTH, DAY, NOW, xsd casts); OPTIONAL, UNION, BIND, VALUES, subqueries; GROUP BY with COUNT, SUM, AVG, MIN, MAX, GROUP_CONCAT, SAMPLE, and HAVING; ORDER BY, LIMIT, OFFSET, DISTINCT; MINUS and FILTER [NOT] EXISTS; property paths made of sequences (p1/p2), inverses (^p), alternatives (p1|p2), negated sets (!p), and one repeated step (p*, p+, p?); SERVICE only on this server's own snapshot endpoints; GeoSPARQL geof: functions with a DuckDB analog (sfIntersects, sfContains, sfWithin, distance, and the like) over geo:asWKT literals.
Not supported: GRAPH, DESCRIBE, blank node syntax in patterns ([ ] and _:b), BASE and relative IRIs, SPARQL Update. A FILTER over a variable nothing binds is an error. Every prefix used must be declared with PREFIX; an undeclared one is an error. The HTTP endpoint applies no implicit LIMIT, so give one unless the user wants everything.`
}

/* ---------- the tools ---------- */

/** What a tool reports to the pane as it runs. */
type ToolReporter = {
  start: (title: string, input: unknown) => number
  finish: (id: number, summary: string, output: unknown, ok: boolean) => void
}

/** A value as the model sees it in a tool result, cut to a readable length. */
function clip(value: string): string {
  return value.length > MAX_VALUE_LENGTH ? `${value.slice(0, MAX_VALUE_LENGTH)}…` : value
}

/** The first keyword of a query after its prologue, to tell an ASK from the forms that take a LIMIT. */
function queryForm(query: string): string {
  const body = query.replace(/^\s*(#[^\n]*\n|(PREFIX|BASE)\s+[^\n]*\n|\s)*/i, '')
  return (/^\w+/.exec(body)?.[0] ?? '').toUpperCase()
}

/** Adds a LIMIT to a draft that states none at the end, so a draft never reads the whole table. */
function bounded(query: string): string {
  if (queryForm(query) === 'ASK') return query
  if (/\bLIMIT\s+\d+\s*(OFFSET\s+\d+\s*)?$/i.test(query.trim())) return query
  return `${query.trim()}\nLIMIT ${DRAFT_LIMIT}`
}

/** A SPARQL JSON result as a tool answers it: the variables and the first rows, a value as text. */
function summarizeResults(results: SparqlResults) {
  if (results.boolean !== undefined) return { boolean: results.boolean }
  const bindings = results.results?.bindings ?? []
  const rows = bindings.slice(0, MAX_ROWS).map((binding) =>
    Object.fromEntries(
      Object.entries(binding).map(([name, value]) => {
        let text = clip(value.value)
        if (value['xml:lang']) text += `@${value['xml:lang']}`
        else if (value.datatype && !/#string$/.test(value.datatype)) text += `^^<${value.datatype}>`
        return [name, text]
      }),
    ),
  )
  return {
    variables: results.head.vars ?? [],
    rows,
    returned: bindings.length,
    ...(bindings.length > MAX_ROWS ? { note: `Only the first ${MAX_ROWS} of ${bindings.length} rows are shown.` } : {}),
  }
}

/** A tool's failure as its result, so the model reads the reason and tries again rather than the turn failing. */
function failed(caught: unknown): { error: string } {
  return { error: caught instanceof Error ? caught.message : String(caught) }
}

/** One triple pattern's worth of overview: the classes in use, or the predicates, each with how often. */
async function usage(query: string, signal: AbortSignal | undefined) {
  const results = await runSparql(query, signal)
  return (results.results?.bindings ?? []).map((binding) => ({
    iri: binding.iri?.value ?? '',
    count: Number(binding.count?.value ?? 0),
  }))
}

function buildTools(vocabularies: Vocabulary[], report: ToolReporter) {
  // Every tool runs the same way: tell the pane it started, answer or
  // report the failure as its result, and tell the pane how it went.
  const run = async <T extends object>(title: string, input: unknown, body: () => Promise<T>, summarize: (output: T) => string) => {
    const id = report.start(title, input)
    try {
      const output = await body()
      const error = 'error' in output && typeof output.error === 'string' ? output.error : null
      report.finish(id, error ?? summarize(output), output, !error)
      return output
    } catch (caught) {
      const output = failed(caught)
      report.finish(id, output.error, output, false)
      return output
    }
  }

  const findTerms = tool({
    name: 'find_terms',
    description:
      'Lists the terms a pinned vocabulary defines, optionally narrowed to the ones whose name or comment contains a search string, each with whether it is a class or a property and its comment. Give a prefix to list one vocabulary, a search to look across all of them, or both.',
    inputSchema: z.object({
      prefix: z.string().optional().describe('The prefix of the vocabulary to list, as declared in the instructions, e.g. "schema".'),
      search: z.string().optional().describe('Case-insensitive text to find in term names and comments.'),
      kind: z.enum(['class', 'property', 'other', 'all']).optional().describe('Keep only classes, only properties, or everything (the default).'),
    }),
    execute: (input) =>
      run(
        input.search ? `Searched terms for “${input.search}”` : `Listed ${input.prefix ?? 'all'}: terms`,
        input,
        async () => {
          if (!input.prefix && !input.search) return { error: 'Give a prefix, a search, or both.' }
          const scope = input.prefix ? [vocabularyFor(vocabularies, input.prefix)].filter((v): v is Vocabulary => !!v) : vocabularies
          if (input.prefix && scope.length === 0) {
            return { error: `No pinned vocabulary has the prefix ${input.prefix}; the instructions list the ones there are.` }
          }
          const needle = input.search?.toLowerCase() ?? ''
          const kind = input.kind ?? 'all'
          const terms: { term: string; kind: string; comment?: string }[] = []
          let total = 0
          for (const vocabulary of scope) {
            for (const term of termsOf(vocabulary)) {
              if (kind !== 'all' && term.kind !== kind) continue
              const comment = commentOf(vocabulary.statements[term.name])
              if (needle && !term.name.toLowerCase().includes(needle) && !comment?.toLowerCase().includes(needle)) continue
              total++
              if (terms.length < MAX_TERMS) terms.push({ term: `${vocabulary.prefix}:${term.name}`, kind: term.kind, comment })
            }
          }
          return { terms, total, ...(total > terms.length ? { note: `Only the first ${MAX_TERMS} of ${total} matches are listed; narrow the search.` } : {}) }
        },
        (output) => ('terms' in output ? `${output.terms.length} of ${output.total} terms` : ''),
      ),
  })

  const describeTerm = tool({
    name: 'describe_term',
    description:
      'Everything the pinned vocabulary document states about one term: its label, comment, superclasses, domain, range, and the like. Takes a prefixed name such as schema:Person.',
    inputSchema: z.object({
      term: z.string().describe('The term as a prefixed name, e.g. "schema:name".'),
    }),
    execute: (input) =>
      run(
        `Looked up ${input.term}`,
        input,
        async () => {
          const match = /^([A-Za-z_][\w.-]*):(.+)$/.exec(input.term.trim())
          if (!match) return { error: 'Give the term as prefix:name.' }
          const vocabulary = vocabularyFor(vocabularies, match[1])
          if (!vocabulary) return { error: `No pinned vocabulary has the prefix ${match[1]}.` }
          const kind = termsOf(vocabulary).find((term) => term.name === match[2])?.kind
          const statements = vocabulary.statements[match[2]] ?? []
          if (!kind && statements.length === 0) {
            return { error: `${vocabulary.prefix}:${match[2]} is not a term of <${vocabulary.namespace}>; find_terms lists the ones there are.` }
          }
          return {
            iri: vocabulary.namespace + match[2],
            kind: kind ?? 'other',
            statements: statements.map((statement) => ({
              predicate: statement.predicate,
              value: clip(statement.value),
              ...(statement.language ? { language: statement.language } : {}),
            })),
          }
        },
        (output) => `${output.kind}, ${output.statements?.length ?? 0} statements`,
      ),
  })

  const schemaOverview = tool({
    name: 'schema_overview',
    description:
      'What the table actually holds: the classes subjects are typed with and the predicates in use, each with how many statements, most used first. With a class, the predicates used on subjects of that class instead. Call this before writing a query about data you have not seen.',
    inputSchema: z.object({
      type: z.string().optional().describe('A class as a prefixed name or IRI; when given, only the predicates its instances carry are listed.'),
    }),
    execute: (input, context) =>
      run(
        input.type ? `Listed the predicates of ${input.type}` : 'Read the classes and predicates in use',
        input,
        async () => {
          const signal = context?.signal
          if (input.type) {
            const iri = expandTerm(vocabularies, input.type)
            const predicates = await usage(
              `SELECT ?iri (COUNT(*) AS ?count) WHERE { ?s <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <${iri}> . ?s ?iri ?o . } GROUP BY ?iri ORDER BY DESC(?count) LIMIT 100`,
              signal,
            )
            return { type: iri, predicates }
          }
          const [classes, predicates] = await Promise.all([
            usage(
              'SELECT ?iri (COUNT(?s) AS ?count) WHERE { ?s <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> ?iri . } GROUP BY ?iri ORDER BY DESC(?count) LIMIT 100',
              signal,
            ),
            usage('SELECT ?iri (COUNT(*) AS ?count) WHERE { ?s ?iri ?o . } GROUP BY ?iri ORDER BY DESC(?count) LIMIT 100', signal),
          ])
          return { classes, predicates }
        },
        (output) =>
          output.classes ? `${output.classes.length} classes, ${output.predicates.length} predicates` : `${output.predicates?.length ?? 0} predicates`,
      ),
  })

  const runQuery = tool({
    name: 'run_sparql',
    description:
      `Runs a SPARQL query against the dataset and answers the variables and the first ${MAX_ROWS} rows, or the boolean of an ASK, or the error the endpoint gave. A query with no LIMIT is given one of ${DRAFT_LIMIT} for the run. Use it to check every draft.`,
    inputSchema: z.object({
      query: z.string().describe('The complete query, PREFIX declarations included.'),
    }),
    execute: (input, context) =>
      run(
        'Ran a query',
        input.query,
        async () => summarizeResults(await runSparql(bounded(input.query), context?.signal)),
        (output) => (output.boolean !== undefined ? `answered ${output.boolean}` : `${output.returned ?? 0} rows`),
      ),
  })

  return [findTerms, describeTerm, schemaOverview, runQuery] as const
}

/* ---------- a turn ---------- */

export type AskContext = {
  settings: AssistantSettings
  vocabularies: Vocabulary[]
  /** The table's snapshot IDs, newest first, for the diff queries. */
  snapshots: string[]
  /** The query the editor holds, described to the model when it has changed since it was last described. */
  editorQuery: string
}

/**
 * Sends one message and streams the answer into the chat. The model may call
 * the tools several times before answering; every call shows as a row of the
 * assistant's message as it happens, and the text as it streams. Rejects
 * nothing: a failure ends the message with its error, and a stop with what
 * had arrived.
 */
export async function ask(text: string, context: AskContext): Promise<void> {
  if (chat.busy) return
  const userId = nextId++
  const assistantId = nextId++
  publish({
    busy: true,
    messages: [
      ...chat.messages,
      { id: userId, role: 'user', text },
      { id: assistantId, role: 'assistant', parts: [], status: 'streaming' },
    ],
  })

  const appendText = (delta: string) =>
    updateAssistant(assistantId, (message) => {
      const last = message.parts[message.parts.length - 1]
      const parts =
        last?.kind === 'text'
          ? [...message.parts.slice(0, -1), { kind: 'text' as const, text: last.text + delta }]
          : [...message.parts, { kind: 'text' as const, text: delta }]
      return { ...message, parts }
    })
  const report: ToolReporter = {
    start: (title, input) => {
      const id = nextId++
      updateAssistant(assistantId, (message) => ({
        ...message,
        parts: [...message.parts, { kind: 'tool', id, title, summary: 'running…', input, status: 'running' }],
      }))
      return id
    },
    finish: (id, summary, output, ok) =>
      updateAssistant(assistantId, (message) => ({
        ...message,
        parts: message.parts.map((part) =>
          part.kind === 'tool' && part.id === id
            ? { ...part, summary, output, status: ok ? 'done' : 'failed' }
            : part,
        ),
      })),
  }

  const editorQuery = context.editorQuery.trim()
  let input = text
  if (editorQuery && editorQuery !== describedQuery) {
    input += `\n\n(The query currently in the editor is:\n\`\`\`sparql\n${editorQuery}\n\`\`\`\n)`
    describedQuery = editorQuery
  }

  const state: StateAccessor = {
    load: async () => conversation,
    save: async (next) => {
      conversation = next
    },
  }
  const abort = new AbortController()
  controller = abort
  const client = new OpenRouter({
    apiKey: context.settings.apiKey,
    appTitle: 'SAL',
    httpReferer: window.location.origin,
    hooks: { beforeCreateRequest: (_, input) => withoutBrowserBlockedHeaders(input) },
  })
  let status: 'done' | 'stopped' | 'failed' = 'done'
  let error: string | undefined
  try {
    const result = client.callModel({
      model: context.settings.model || DEFAULT_MODEL,
      instructions: instructions(context.vocabularies, context.snapshots),
      input,
      tools: buildTools(context.vocabularies, report),
      state,
      stopWhen: stepCountIs(MAX_STEPS),
      signal: abort.signal,
    })
    for await (const delta of result.getTextStream()) appendText(delta)
    // The stream ends with the run; this surfaces an error the stream swallowed.
    await result.getResponse()
  } catch (caught) {
    if (abort.signal.aborted) {
      status = 'stopped'
    } else {
      status = 'failed'
      error = describeFailure(caught)
    }
  } finally {
    if (controller === abort) controller = null
    updateAssistant(assistantId, (message) => ({
      ...message,
      status,
      error,
      // A tool still marked running when the turn ends was cut off with it.
      parts: message.parts.map((part) => (part.kind === 'tool' && part.status === 'running' ? { ...part, status: 'failed', summary: 'stopped' } : part)),
    }))
    publish({ ...chat, busy: false })
  }
}

/**
 * The headers the toolkit adds that openrouter.ai's CORS preflight does not
 * allow from a browser. The agent loop stamps every request with
 * x-openrouter-callmodel, which is not in the Access-Control-Allow-Headers
 * list the API answers, so a browser refuses to send the request at all; the
 * header is telemetry, and the request works without it. It is taken out of
 * the headers the SDK is about to build the Request from, in place, rather
 * than by rebuilding the Request afterwards, which Firefox was seen to send
 * without its Authorization header.
 */
const BROWSER_BLOCKED_HEADERS = ['x-openrouter-callmodel']

type RequestInput = { url: URL; options?: RequestInit }

function withoutBrowserBlockedHeaders(input: RequestInput): RequestInput {
  const headers = input.options?.headers
  if (headers instanceof Headers) {
    for (const name of BROWSER_BLOCKED_HEADERS) headers.delete(name)
    return input
  }
  const kept = new Headers(headers ?? undefined)
  for (const name of BROWSER_BLOCKED_HEADERS) kept.delete(name)
  return { ...input, options: { ...input.options, headers: kept } }
}

/** What the SDK's error classes carry of OpenRouter's error body: the message, the status, and for a provider's failure what the provider said. */
type OpenRouterFailure = {
  statusCode?: number
  status?: number
  error?: { message?: string; code?: number; metadata?: { raw?: string; provider_name?: string } }
}

/**
 * A failed turn's message: OpenRouter's own, followed by what the upstream
 * provider said when there is more to it (a free model rate-limited upstream
 * answers only "Provider returned error" otherwise), with a hint for the
 * failures a user can fix themselves.
 */
function describeFailure(caught: unknown): string {
  let message = caught instanceof Error ? caught.message : String(caught)
  const failure = (caught ?? {}) as OpenRouterFailure
  const status = failure.statusCode ?? failure.status ?? failure.error?.code
  const raw = failure.error?.metadata?.raw
  if (raw && !message.includes(raw)) message += ` (${failure.error?.metadata?.provider_name ?? 'provider'}: ${raw})`
  if (status === 401 || /unauthorized|invalid api key|no auth credentials/i.test(message)) {
    return `OpenRouter refused the API key (${message}). Check it in the settings.`
  }
  if (status === 402 || /insufficient credits/i.test(message)) {
    return `OpenRouter reports no credit for this key (${message}).`
  }
  if (status === 404 || /not a valid model/i.test(message)) {
    return `OpenRouter does not know the model (${message}). Pick another in the settings.`
  }
  if (status === 429 || /rate.limited/i.test(message)) {
    return `${message} Try again in a moment, or pick another model in the settings.`
  }
  return message
}
