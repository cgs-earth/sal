import { HelpTip } from './HelpTip'
import { VALUE_KINDS, type PropertyDraft, type ShapeKind } from './document'

/**
 * The structured editor of a shape's properties: one card per sh:property
 * with the fields the form knows, which is what a module needs in nearly
 * every case. The JSON mode beside it is for the rest of SHACL.
 */
export function PropertyRows({
  kind,
  properties,
  onChange,
}: {
  kind: ShapeKind
  properties: PropertyDraft[]
  onChange: (properties: PropertyDraft[]) => void
}) {
  const update = (key: number, patch: Partial<PropertyDraft>) =>
    onChange(properties.map((property) => (property.key === key ? { ...property, ...patch } : property)))
  const task = kind === 'taskShape'
  if (properties.length === 0) {
    return (
      <p className="hint property-empty">
        {task ? 'No configuration properties: an instance carries only its ID and type.' : 'No property constraints: any node of the target class passes.'}
      </p>
    )
  }
  return (
    <div className="property-rows">
      {properties.map((property, index) => {
        const id = `property-${property.key}`
        return (
          <div key={property.key} className="property-card">
            <div className="property-row">
              <div className="property-field">
                <label htmlFor={`${id}-name`}>
                  Property
                  <HelpTip label="the property name">
                    {task
                      ? 'A bare name such as maxRetries. It becomes a term of the module, declared with its type and range where it is constrained, and is the key the task instance carries it under.'
                      : 'A term of a vocabulary the prefixes bind, such as dcterms:title, or a full IRI: the key the task prints it under.'}
                  </HelpTip>
                </label>
                <input
                  id={`${id}-name`}
                  className="mono"
                  placeholder={task ? 'maxRetries' : 'dcterms:title'}
                  spellCheck={false}
                  value={property.path}
                  onChange={(event) => update(property.key, { path: event.target.value })}
                />
              </div>
              <div className="property-field">
                <label htmlFor={`${id}-kind`}>
                  Value
                  <HelpTip label="the value kind">
                    What each value is: a literal of this datatype (sh:datatype), an IRI naming another node (sh:nodeKind sh:IRI), or
                    anything. {task && 'A configuration property is declared with the matching rdfs:range.'}
                  </HelpTip>
                </label>
                <select id={`${id}-kind`} value={property.kind} onChange={(event) => update(property.key, { kind: event.target.value as PropertyDraft['kind'] })}>
                  {VALUE_KINDS.map((entry) => (
                    <option key={entry.kind} value={entry.kind}>
                      {entry.label}
                    </option>
                  ))}
                </select>
              </div>
              <div className="property-field property-counts">
                <span className="property-counts-label">
                  How many
                  <HelpTip label="how many values">
                    Required means at least one value (sh:minCount 1); otherwise the property may be left out. One value means at most
                    one (sh:maxCount 1); otherwise it may be repeated.
                  </HelpTip>
                </span>
                <div className="property-checks">
                  <label className="toggle">
                    <input type="checkbox" checked={property.required} onChange={(event) => update(property.key, { required: event.target.checked })} />
                    Required
                  </label>
                  <label className="toggle">
                    <input type="checkbox" checked={property.single} onChange={(event) => update(property.key, { single: event.target.checked })} />
                    At most one
                  </label>
                </div>
              </div>
              <button type="button" className="button small property-remove" onClick={() => onChange(properties.filter((other) => other.key !== property.key))}>
                Remove
              </button>
            </div>
            <div className="property-row property-text">
              <div className="property-field">
                <label htmlFor={`${id}-comment`}>Description</label>
                <input
                  id={`${id}-comment`}
                  placeholder={task ? 'What the property configures (rdfs:comment)' : 'What the constraint is for (sh:description)'}
                  value={property.comment}
                  onChange={(event) => update(property.key, { comment: event.target.value })}
                />
              </div>
              <div className="property-field">
                <label htmlFor={`${id}-message`}>Violation message</label>
                <input
                  id={`${id}-message`}
                  placeholder={`Shown when property ${index + 1} is violated (sh:message)`}
                  value={property.message}
                  onChange={(event) => update(property.key, { message: event.target.value })}
                />
              </div>
            </div>
          </div>
        )
      })}
    </div>
  )
}
