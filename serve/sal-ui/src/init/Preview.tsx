import { useEffect, useRef } from 'react'
import CodeMirror from '@uiw/react-codemirror'
import { json } from '@codemirror/lang-json'
import { lintGutter, setDiagnostics, type Diagnostic } from '@codemirror/lint'
import { EditorView } from '@codemirror/view'
import { editorExtensions } from '../theme'
import type { Problem } from './api'

const extensions = [json(), lintGutter(), EditorView.editable.of(false), ...editorExtensions]

/**
 * The ontology.jsonld exactly as it would be written, read only, with the
 * problems the server reported marked on their lines. The text is what the
 * server was sent, so the lines agree.
 */
export function Preview({ text, problems }: { text: string; problems: Problem[] }) {
  const view = useRef<EditorView | null>(null)

  // The diagnostics are set after every change rather than through a linter
  // extension, since they come from the server rather than from the text.
  useEffect(() => {
    const editor = view.current
    if (!editor) return
    const doc = editor.state.doc
    const diagnostics: Diagnostic[] = problems.map((problem) => {
      const line = doc.line(Math.min(Math.max(problem.line, 1), doc.lines))
      return { from: line.from, to: line.to, severity: 'error', message: problem.message }
    })
    editor.dispatch(setDiagnostics(editor.state, diagnostics))
  }, [text, problems])

  return (
    <div className="editor init-preview-editor">
      <CodeMirror
        value={text}
        readOnly
        extensions={extensions}
        basicSetup={{ foldGutter: false, highlightActiveLine: false, highlightActiveLineGutter: false }}
        onCreateEditor={(editor) => {
          view.current = editor
        }}
      />
    </div>
  )
}
