import { failure } from '../api'

/** What sal knows about the module the form is open on. */
export type ModuleStatus = {
  /** The command that opened the form, which the page names itself after. */
  mode: 'init' | 'edit'
  /** Whether ontology.jsonld exists in the module root. */
  initialized: boolean
  /** Where ontology.jsonld is, or will be, written. */
  ontologyPath: string
  /** The module's own salmodule:// namespace, derived from the git remote. */
  namespace: string
  /** The git repository name, offered as a new ontology's dcterms:title. */
  title: string
  /** The git user.name, offered as a new ontology's dcterms:creator; empty when git has none. */
  creator: string
  /** The current text of ontology.jsonld, which the form edits from, or "" when there is none. */
  document: string
  /** Whether the module has a Dockerfile yet. */
  dockerfile: boolean
}

/** One problem sal found with the assembled document, on a line of the document as the preview shows it (0 for the document as a whole). */
export type Problem = {
  line: number
  message: string
}

export async function fetchStatus(signal?: AbortSignal): Promise<ModuleStatus> {
  const response = await fetch('/api/module/status', { signal })
  if (!response.ok) throw await failure(response)
  return (await response.json()) as ModuleStatus
}

async function postDocument(path: string, body: { document: string; base?: string }, signal?: AbortSignal): Promise<Response> {
  const response = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal,
  })
  if (!response.ok) throw await failure(response)
  return response
}

/**
 * Runs every check `sal validate --salmodule` makes over the document, without
 * writing anything. The text sent is the exact text the preview shows, so the
 * lines come back meaning the preview's lines.
 */
export async function validateOntology(document: string, signal?: AbortSignal): Promise<Problem[]> {
  const response = await postDocument('/api/module/validate', { document }, signal)
  return ((await response.json()) as { problems: Problem[] }).problems
}

/**
 * Writes the document as ontology.jsonld, with the Dockerfile, .dockerignore,
 * and editor schema `sal init --salmodule` scaffolds beside it where they are
 * missing. base is the text the form loaded from disk: the server refuses to
 * write over a file that no longer holds it, since it was edited by hand
 * since, and refuses a document with problems.
 */
export async function writeOntology(document: string, base: string): Promise<string> {
  const response = await postDocument('/api/module/write', { document, base })
  return ((await response.json()) as { ontologyPath: string }).ontologyPath
}
