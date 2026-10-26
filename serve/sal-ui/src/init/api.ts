import { failure } from '../api'

/** What `sal init --salmodule --ui` knows about the module it is initializing. */
export type InitStatus = {
  /** Whether ontology.jsonld already exists in the module root, which locks the form. */
  initialized: boolean
  /** Where ontology.jsonld is, or will be, written. */
  ontologyPath: string
  /** The module's own salmodule:// namespace, derived from the git remote. */
  namespace: string
  /** The git repository name, offered as the ontology's dc:title. */
  title: string
  /** The git user.name, offered as the ontology's dc:creator; empty when git has none. */
  creator: string
}

/** One problem sal found with the assembled document, on a line of the document as the preview shows it (0 for the document as a whole). */
export type Problem = {
  line: number
  message: string
}

export async function fetchInitStatus(signal?: AbortSignal): Promise<InitStatus> {
  const response = await fetch('/api/init/status', { signal })
  if (!response.ok) throw await failure(response)
  return (await response.json()) as InitStatus
}

/**
 * Runs every check `sal validate --salmodule` makes over the document, without
 * writing anything. The text sent is the exact text the preview shows, so the
 * lines come back meaning the preview's lines.
 */
export async function validateOntology(document: string, signal?: AbortSignal): Promise<Problem[]> {
  const response = await fetch('/api/init/validate', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ document }),
    signal,
  })
  if (!response.ok) throw await failure(response)
  return ((await response.json()) as { problems: Problem[] }).problems
}

/**
 * Writes the document as ontology.jsonld, with the Dockerfile, .dockerignore,
 * and editor schema `sal init --salmodule` scaffolds beside it. The server
 * checks the document again and refuses one with problems, and refuses to
 * overwrite an ontology.jsonld that already exists.
 */
export async function writeOntology(document: string): Promise<string> {
  const response = await fetch('/api/init/write', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ document }),
  })
  if (!response.ok) throw await failure(response)
  return ((await response.json()) as { ontologyPath: string }).ontologyPath
}
