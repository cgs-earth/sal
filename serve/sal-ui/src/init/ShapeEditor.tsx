import CodeMirror from '@uiw/react-codemirror'
import { json, jsonParseLinter } from '@codemirror/lang-json'
import { linter, lintGutter } from '@codemirror/lint'
import { editorExtensions } from '../theme'
import {
  EXAMPLE_TARGET,
  exampleProperty,
  exampleShapeText,
  newProperty,
  shapeRows,
  shapeTemplateText,
  shapeText,
  type ShapeDraft,
  type ShapeKind,
} from './document'
import { PropertyRows } from './PropertyRows'

/** The JSON editor marks a syntax error where it is, before the server is asked anything. */
const extensions = [json(), linter(jsonParseLinter()), lintGutter(), ...editorExtensions]

/**
 * The editor of one SHACL shape. In form mode it is the target class and a
 * row per property; in JSON mode it is the shape as JSON-LD using the
 * prefixes the form binds, for pasting SHACL in or for whatever the rows
 * cannot say. A shape is declared as rows or straight as JSON; switching to
 * JSON renders the rows, and switching back reads the JSON into rows when it
 * holds nothing else, the button saying when it does not. An undeclared
 * shape is left out of the document.
 */
export function ShapeEditor({
  id,
  kind,
  taskID,
  shape,
  onChange,
}: {
  id: string
  kind: ShapeKind
  taskID: string
  shape: ShapeDraft
  onChange: (shape: ShapeDraft) => void
}) {
  const declare = (patch: Partial<ShapeDraft>) => onChange({ ...shape, declared: true, ...patch })

  if (!shape.declared) {
    return (
      <div className="shape-editor">
        <div className="shape-editor-actions">
          <button type="button" className="button small" onClick={() => declare({ mode: 'form' })}>
            Declare
          </button>
          <button
            type="button"
            className="button small"
            onClick={() => declare({ mode: 'form', targetClass: kind === 'taskShape' ? '' : EXAMPLE_TARGET, properties: [exampleProperty(kind)] })}
          >
            Insert example
          </button>
          <button
            type="button"
            className="button small"
            title="Declare the shape as JSON-LD, to paste SHACL into"
            onClick={() => declare({ mode: 'json', json: shapeTemplateText(kind, taskID) })}
          >
            Declare as JSON
          </button>
          <span className="hint">Not declared</span>
        </div>
      </div>
    )
  }

  if (shape.mode === 'json') {
    const rows = shapeRows(kind, taskID, shape.json)
    return (
      <div className="shape-editor">
        <div className="shape-editor-actions">
          <button
            type="button"
            className="button small"
            disabled={!rows}
            title={rows ? 'Show the shape as rows' : 'The JSON holds constraints the rows cannot show, so it stays JSON'}
            onClick={() => rows && onChange(rows)}
          >
            Edit as form
          </button>
          <button type="button" className="button small" onClick={() => onChange({ ...shape, json: exampleShapeText(kind, taskID) })}>
            Insert example
          </button>
          <button type="button" className="button small" onClick={() => onChange({ ...shape, declared: false })}>
            Remove shape
          </button>
          <span className="hint">{rows ? 'JSON' : 'JSON (not representable as rows)'}</span>
        </div>
        <CodeMirror
          id={id}
          value={shape.json}
          minHeight="80px"
          maxHeight="360px"
          placeholder="A sh:NodeShape as JSON-LD, using the prefixes bound above"
          extensions={extensions}
          basicSetup={{ foldGutter: false }}
          onChange={(json) => onChange({ ...shape, json })}
        />
      </div>
    )
  }

  return (
    <div className="shape-editor">
      <div className="shape-editor-actions">
        <button type="button" className="button small" onClick={() => onChange({ ...shape, properties: [...shape.properties, newProperty(kind)] })}>
          Add property
        </button>
        <button
          type="button"
          className="button small"
          onClick={() =>
            onChange({
              ...shape,
              targetClass: kind === 'taskShape' || shape.targetClass ? shape.targetClass : EXAMPLE_TARGET,
              properties: [...shape.properties, exampleProperty(kind)],
            })
          }
        >
          Insert example
        </button>
        <button
          type="button"
          className="button small"
          title="Edit the shape as JSON-LD, for constraints the rows cannot say"
          onClick={() => onChange({ ...shape, mode: 'json', json: shapeText(kind, taskID, shape) })}
        >
          Edit as JSON
        </button>
        <button type="button" className="button small" onClick={() => onChange({ ...shape, declared: false })}>
          Remove shape
        </button>
        <span className="hint">Declared</span>
      </div>
      <div className="shape-form">
        {kind === 'taskShape' ? (
          <p className="hint shape-target-note">
            Targets the task class <code>{taskID.trim() || '(the task ID)'}</code>: these are the properties an instance is configured with.
          </p>
        ) : (
          <div className="shape-target">
            <label htmlFor={`${id}-target`}>
              Target class <code>sh:targetClass</code>
            </label>
            <input
              id={`${id}-target`}
              className="mono"
              placeholder={EXAMPLE_TARGET}
              spellCheck={false}
              value={shape.targetClass}
              onChange={(event) => onChange({ ...shape, targetClass: event.target.value })}
            />
            <span className="hint">The @type of the nodes the shape applies to; empty applies it to none, so set it.</span>
          </div>
        )}
        <PropertyRows kind={kind} properties={shape.properties} onChange={(properties) => onChange({ ...shape, properties })} />
      </div>
    </div>
  )
}
