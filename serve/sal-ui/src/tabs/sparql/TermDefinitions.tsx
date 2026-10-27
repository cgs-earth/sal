import type { TermInfo } from './completers'

/**
 * The Definitions pane docked down the right of the SPARQL editor, which
 * the editor is narrowed to make room for (see .sal-term-panel in
 * yasgui-theme.css). It describes the term the editor is on, the highlighted
 * suggestion or the one under the caret: the name with its kind, the IRI it
 * expands to, and what the pinned document states about it as a definition
 * list, a literal's language tagged after it and an IRI value set as code.
 * With nothing to describe it says what to do.
 */
export function TermDefinitions({ term }: { term: TermInfo | null }) {
  if (!term) {
    return (
      <p className="sal-term-panel-empty">
        Place the caret on a term to introspect its definition
      </p>
    )
  }
  return (
    <>
      <div className="sal-term-panel-head">
        <code>{term.name}</code>
        {term.kind !== 'other' && <span className="sal-hint-kind">{term.kind}</span>}
      </div>
      <div className="sal-term-panel-iri">{term.iri}</div>
      {term.statements.length === 0 ? (
        <p className="sal-term-panel-empty">The pinned document says nothing more about this term.</p>
      ) : (
        <dl className="sal-term-panel-statements">
          {term.statements.map((statement, i) => (
            // A predicate is shown once, above its first value; the values
            // of the same predicate line up under it.
            <Statement key={i} {...statement} first={i === 0 || term.statements[i - 1].predicate !== statement.predicate} />
          ))}
        </dl>
      )}
    </>
  )
}

function Statement({
  predicate,
  value,
  iri,
  language,
  first,
}: TermInfo['statements'][number] & { first: boolean }) {
  return (
    <>
      {first && <dt>{predicate}</dt>}
      <dd>
        {iri ? (
          <code>{value}</code>
        ) : (
          <>
            {value}
            {language && <span className="sal-term-panel-lang"> @{language}</span>}
          </>
        )}
      </dd>
    </>
  )
}
