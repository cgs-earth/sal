import CodeMirror from '@uiw/react-codemirror'
import { json } from '@codemirror/lang-json'
import { editorExtensions } from '../theme'

const extensions = [json(), ...editorExtensions]

/**
 * The editor a SHACL shape is written in, as JSON-LD using the prefixes the
 * form binds. Empty means the task does not declare the shape; the buttons
 * above it fill in a worked example to edit from or clear it again.
 */
export function ShapeEditor({
  id,
  value,
  example,
  onChange,
}: {
  id: string
  value: string
  /** The example the Insert example button fills in. */
  example: string
  onChange: (value: string) => void
}) {
  return (
    <div className="shape-editor">
      <div className="shape-editor-actions">
        <button type="button" className="button small" onClick={() => onChange(example)}>
          Insert example
        </button>
        <button type="button" className="button small" disabled={!value} onClick={() => onChange('')}>
          Clear
        </button>
        <span className="hint">{value ? 'Declared' : 'Not declared'}</span>
      </div>
      <CodeMirror
        id={id}
        value={value}
        minHeight="80px"
        maxHeight="360px"
        placeholder="Leave empty to declare no shape, or insert the example"
        extensions={extensions}
        basicSetup={{ foldGutter: false }}
        onChange={onChange}
      />
    </div>
  )
}
