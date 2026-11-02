import type { Vocabulary } from '../../api'

/** The `prefix:local` split of what was typed, when the prefix is one of the vocabularies'. */
export function splitCurie(text: string, vocabularies: Vocabulary[]): { vocabulary: Vocabulary; local: string } | null {
  const match = /^([A-Za-z_][\w.-]*):(.*)$/.exec(text)
  if (!match) return null
  const vocabulary = vocabularies.find((candidate) => candidate.prefix === match[1])
  return vocabulary ? { vocabulary, local: match[2] } : null
}

/**
 * Expands what was typed to the IRI it names: a `prefix:local` against the
 * vocabularies' prefixes, or an absolute IRI as it is. Null when it is
 * neither, so a bare word is never sent to the endpoint.
 */
export function expandTerm(text: string, vocabularies: Vocabulary[]): string | null {
  const trimmed = text.trim()
  if (!trimmed) return null
  const curie = splitCurie(trimmed, vocabularies)
  if (curie) return curie.local ? curie.vocabulary.namespace + curie.local : null
  return /^[a-z][a-z0-9+.-]*:\S+$/i.test(trimmed) && (trimmed.includes('//') || trimmed.startsWith('urn:'))
    ? trimmed
    : null
}

/** The `prefix:local` form of an IRI under one of the vocabularies, else the IRI itself. */
export function compactTerm(iri: string, vocabularies: Vocabulary[]): string {
  for (const vocabulary of vocabularies) {
    if (iri.startsWith(vocabulary.namespace) && iri.length > vocabulary.namespace.length) {
      return `${vocabulary.prefix}:${iri.slice(vocabulary.namespace.length)}`
    }
  }
  return iri
}
