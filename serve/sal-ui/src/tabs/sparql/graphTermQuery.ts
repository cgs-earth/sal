import type { GraphNodeAttributes } from './graphData'

const LIMIT = 500

/** Escapes a literal's value for embedding in a SPARQL double-quoted string. */
function escapeSparqlString(value: string): string {
  return value.replace(/\\/g, '\\\\').replace(/"/g, '\\"').replace(/\n/g, '\\n').replace(/\r/g, '\\r').replace(/\t/g, '\\t')
}

function serializeLiteral(attrs: GraphNodeAttributes): string {
  const literal = `"${escapeSparqlString(attrs.fullLabel)}"`
  if (attrs.lang) return `${literal}@${attrs.lang}`
  if (attrs.datatype) return `${literal}^^<${attrs.datatype}>`
  return literal
}

/**
 * Whether a node's term can be reused in a new SPARQL query: a URI (as a
 * subject) or a literal (as an object). A blank node's label isn't a term
 * another query can address, and a root node is a synthetic stand-in for a
 * query constant with no real term behind it at all.
 */
export function canQueryFromNode(attrs: GraphNodeAttributes): boolean {
  return attrs.kind === 'uri' || attrs.kind === 'literal'
}

/**
 * Builds a simple SPARQL query around a node's term: an IRI becomes the
 * subject of a `?predicate ?object` pattern, a literal becomes the object of
 * a `?subject ?predicate` pattern, each capped at LIMIT rows so the graph
 * this seeds stays small enough to lay out.
 */
export function buildTermQuery(attrs: GraphNodeAttributes): string {
  if (attrs.kind === 'uri') {
    return `SELECT ?predicate ?object\nWHERE {\n  <${attrs.fullLabel}> ?predicate ?object .\n}\nLIMIT ${LIMIT}`
  }
  return `SELECT ?subject ?predicate\nWHERE {\n  ?subject ?predicate ${serializeLiteral(attrs)} .\n}\nLIMIT ${LIMIT}`
}
