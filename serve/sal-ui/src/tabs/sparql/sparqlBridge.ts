/**
 * Lets the graph (rendered deep inside Yasr's plugin tree, with no props path
 * back to the Yasgui instance that owns the active tab) reach up to the tab:
 * hand it a generated query to run, and read back the query text it's
 * currently showing. SparqlTab registers both on mount and clears them on
 * unmount; only one SparqlTab is ever mounted at a time, so a pair of
 * module-level slots is enough.
 */
let runner: ((query: string) => void) | null = null
let queryTextGetter: (() => string) | null = null

export function setQueryRunner(next: ((query: string) => void) | null) {
  runner = next
}

export function setQueryTextGetter(next: (() => string) | null) {
  queryTextGetter = next
}

/** Runs query in the active SPARQL tab, replacing whatever it currently holds. */
export function runGeneratedQuery(query: string) {
  runner?.(query)
}

/** The query text the active SPARQL tab is currently showing, or '' if none is mounted. */
export function getCurrentQueryText(): string {
  return queryTextGetter?.() ?? ''
}
