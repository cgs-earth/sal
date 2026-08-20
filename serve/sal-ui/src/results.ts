import { useSyncExternalStore } from 'react'

export type ResultSource = 'SQL' | 'SPARQL'

/** The last result an editor produced, in the header/rows shape both editors can give. */
export type PublishedResult = {
  source: ResultSource
  query: string
  header: string[]
  rows: string[][]
}

export type PublishedResults = {
  SQL: PublishedResult | null
  SPARQL: PublishedResult | null
  /** Whichever editor ran most recently, or null before either has. */
  latest: ResultSource | null
}

/*
 * The SQL and SPARQL tabs publish their latest result here so that the Map tab,
 * which is mounted only while it is shown, can draw whatever geometry the last
 * query returned. A module-level store rather than state lifted into App: only
 * the map reads it, and a tab should keep its last result through the
 * unmount/remount of switching tabs.
 */
let results: PublishedResults = { SQL: null, SPARQL: null, latest: null }
const listeners = new Set<() => void>()

export function publishResult(result: PublishedResult) {
  results = { ...results, [result.source]: result, latest: result.source }
  for (const listener of listeners) listener()
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

export function useResults(): PublishedResults {
  return useSyncExternalStore(subscribe, () => results)
}
