export type QueryResult = {
  sql: string
  header: string[] | null
  rows: string[][] | null
  message: string
}

export type TableStats = {
  tablePath: string
  triples: number
  subjects: number
  predicates: number
  objects: number
  snapshots: QueryResult
  properties: QueryResult
  columnStats: QueryResult
  /** The `salmodule://` URIs of the modules the build that wrote this table downloaded. */
  modules: string[] | null
  /** The `owl:imports` IRIs the project's `.sal/ontology.jsonld` records. */
  imports: string[] | null
  /** The imported data products, each queryable as a view of its own. */
  importedTables: ImportedTable[] | null
  /** Sample queries only the server can write, such as time travel and the import listing. */
  sampleQueries: NamedQuery[] | null
}

/** An imported SAL data product, registered as a DuckDB view named after its OCI artifact. */
export type ImportedTable = {
  view: string
  artifact: string
  path: string
}

export type NamedQuery = {
  name: string
  sql: string
}

export type ModuleOntology = {
  /** The salmodule:// namespace the module's own terms resolve against. */
  module: string
  /** The JSON-LD document the module printed in response to its ontology command. */
  ontology: unknown
}

export type BlobResult = {
  /** The SHA-256 digest the blob was resolved by, with any urn:sha256: prefix stripped. */
  digest: string
  blob: Blob
  contentType: string | null
}

export type GeoJSONFeatureCollection = {
  type: 'FeatureCollection'
  features: {
    type: 'Feature'
    geometry: unknown
    properties: Record<string, string>
  }[]
}

/** Reads the `{"error": "..."}` body the Go handlers send, falling back to the status text. */
async function failure(response: Response): Promise<Error> {
  const body = await response.text()
  try {
    const parsed = JSON.parse(body) as { error?: string }
    if (parsed.error) return new Error(parsed.error)
  } catch {
    // Not a JSON body; fall through to the raw text.
  }
  return new Error(body.trim() || `${response.status} ${response.statusText}`)
}

export async function fetchStats(signal?: AbortSignal): Promise<TableStats> {
  const response = await fetch('/api/stats', { signal })
  if (!response.ok) throw await failure(response)
  return (await response.json()) as TableStats
}

export async function runSQL(sql: string, signal?: AbortSignal): Promise<QueryResult> {
  const response = await fetch('/api/sql', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ sql }),
    signal,
  })
  if (!response.ok) throw await failure(response)
  return (await response.json()) as QueryResult
}

/**
 * Clones, builds, and runs a SAL module so that it publishes its ontology. The
 * first inspection of a module can take minutes, since its image has to be built.
 */
export async function inspectModule(module: string, signal?: AbortSignal): Promise<ModuleOntology> {
  const response = await fetch(`/api/salmodule?module=${encodeURIComponent(module)}`, { signal })
  if (!response.ok) throw await failure(response)
  return (await response.json()) as ModuleOntology
}

/**
 * Fetches the vocabulary or imported ontology document a project pinned by its
 * SHA-256 digest. The server accepts the digest with or without a urn:sha256:
 * prefix, so it is stripped client side too, both to normalize what is sent
 * and to name the downloaded file by the bare digest.
 */
export async function resolveBlob(hash: string, signal?: AbortSignal): Promise<BlobResult> {
  const digest = hash.trim().replace(/^urn:sha256:/i, '')
  if (!digest) throw new Error('Enter a blob hash')
  const response = await fetch(`/blob/${encodeURIComponent(digest)}`, { signal })
  if (!response.ok) throw await failure(response)
  const blob = await response.blob()
  return { digest, blob, contentType: response.headers.get('Content-Type') }
}

export async function fetchGeometries(
  limit: number,
  offset: number,
  signal?: AbortSignal,
): Promise<GeoJSONFeatureCollection> {
  const response = await fetch(`/geometries?limit=${limit}&offset=${offset}`, {
    headers: { Accept: 'application/geo+json' },
    signal,
  })
  if (!response.ok) throw await failure(response)
  return (await response.json()) as GeoJSONFeatureCollection
}
