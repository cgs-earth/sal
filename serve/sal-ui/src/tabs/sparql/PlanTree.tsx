import type { PlanNode } from '../../api'

/** The detail DuckDB writes the rows it expects an operator to produce under. */
const CARDINALITY = 'Estimated Cardinality'

/**
 * A DuckDB plan drawn as a tree: each operator with what DuckDB says about it,
 * and under it the operators it reads from, so the root is read first and the
 * scans last, the way DuckDB's own EXPLAIN draws it. An operator folds on its
 * heading, taking its inputs with it.
 */
export function PlanTree({ roots }: { roots: PlanNode[] }) {
  if (roots.length === 0) return <p className="empty">DuckDB reported no operators for this statement.</p>
  return (
    <ul className="plan-tree">
      {roots.map((root, index) => (
        <PlanOperator key={index} node={root} />
      ))}
    </ul>
  )
}

function PlanOperator({ node }: { node: PlanNode }) {
  const cardinality = node.details.find((detail) => detail.key === CARDINALITY)
  const details = node.details.filter((detail) => detail !== cardinality)
  return (
    <li className="plan-node">
      <details open>
        <summary>
          <span className="plan-name">{node.name}</span>
          {cardinality && (
            <span className="plan-cardinality" title="The rows DuckDB expects this operator to produce">
              ~{cardinality.values.map(formatRows).join(' ')} rows
            </span>
          )}
        </summary>
        {details.length > 0 && (
          <dl className="plan-details">
            {details.map((detail) => (
              <div key={detail.key}>
                <dt>{detail.key}</dt>
                <dd>
                  {detail.values.map((value, index) => (
                    <code key={index}>{value}</code>
                  ))}
                </dd>
              </div>
            ))}
          </dl>
        )}
        {node.children.length > 0 && (
          <ul className="plan-children">
            {node.children.map((child, index) => (
              <PlanOperator key={index} node={child} />
            ))}
          </ul>
        )}
      </details>
    </li>
  )
}

/** A cardinality with thousands separators when it is a number, as written otherwise. */
function formatRows(value: string): string {
  const count = Number(value)
  return Number.isFinite(count) && value.trim() !== '' ? count.toLocaleString() : value
}
