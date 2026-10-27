import { useEffect, useMemo, useState } from 'react'
import { validateOntology, writeOntology, type InitStatus, type Problem } from './api'
import {
  assemble,
  BASE_PREFIXES,
  newDraft,
  newPrefix,
  newTask,
  SHAPE_KINDS,
  shapeExample,
  taskShapeTemplate,
  type ModuleDraft,
  type PrefixDraft,
  type ShapeKind,
  type TaskDraft,
} from './document'
import { HelpTip } from './HelpTip'
import { ShapeEditor } from './ShapeEditor'
import { Preview } from './Preview'

/** How long the form waits after the last edit before asking the server to check the document. */
const CHECK_DEBOUNCE_MS = 400

const SHAPE_HELP: Record<ShapeKind, { title: string; help: string }> = {
  taskShape: {
    title: 'Task shape',
    help: 'A SHACL node shape describing an instance of the task as a project configures it: one sh:property per configuration property, with its datatype and how many values it takes. Keep sh:targetClass as the task ID. The example adds a maxRetries integer; declare each property with an @id, @type, rdfs:domain, and rdfs:range so projects can look it up.',
  },
  stdoutShape: {
    title: 'Stdout shape',
    help: 'The shape every node the task prints must satisfy. sal checks each line of output against it as the task runs and fails the build on the first violation; a task without one runs unchecked, with a warning. sh:languageIn, sh:uniqueLang, sh:disjoint, sh:qualifiedValueShapesDisjoint, and sh:sparql are refused here.',
  },
  stdinShape: {
    title: 'Stdin shape',
    help: 'The shape of the nodes the task reads on standard input, for a task that consumes what another task produced. Leave it empty for a task that only produces.',
  },
}

/**
 * The form that drafts a module ontology and writes it. The draft is
 * assembled into the ontology.jsonld text on every edit; what the form can
 * tell is wrong is listed at once, and otherwise the text goes to the server
 * for the checks that need the SAL Module specification. Write is offered
 * only for a text the server checked and found nothing wrong with.
 */
export function ModuleForm({ status }: { status: InitStatus }) {
  const [draft, setDraft] = useState<ModuleDraft>(() => newDraft(status.title, status.creator))
  const assembled = useMemo(() => assemble(draft), [draft])
  const [serverProblems, setServerProblems] = useState<Problem[]>([])
  // the text serverProblems are about, so a text edited since is not written
  const [checkedText, setCheckedText] = useState<string | null>(null)
  const [checking, setChecking] = useState(false)
  const [checkError, setCheckError] = useState<string | null>(null)
  const [writing, setWriting] = useState(false)
  const [writeError, setWriteError] = useState<string | null>(null)

  useEffect(() => {
    if (assembled.problems.length > 0) {
      setServerProblems([])
      setCheckedText(null)
      setChecking(false)
      return
    }
    const controller = new AbortController()
    setChecking(true)
    const timer = setTimeout(async () => {
      try {
        const problems = await validateOntology(assembled.text, controller.signal)
        setServerProblems(problems)
        setCheckedText(assembled.text)
        setCheckError(null)
      } catch (caught) {
        if (controller.signal.aborted) return
        setCheckError(caught instanceof Error ? caught.message : String(caught))
      } finally {
        if (!controller.signal.aborted) setChecking(false)
      }
    }, CHECK_DEBOUNCE_MS)
    return () => {
      clearTimeout(timer)
      controller.abort()
    }
  }, [assembled])

  const update = (patch: Partial<ModuleDraft>) => setDraft((current) => ({ ...current, ...patch }))
  const updatePrefix = (key: number, patch: Partial<PrefixDraft>) =>
    update({ prefixes: draft.prefixes.map((entry) => (entry.key === key ? { ...entry, ...patch } : entry)) })
  const updateTask = (key: number, patch: Partial<TaskDraft>) =>
    update({ tasks: draft.tasks.map((task) => (task.key === key ? { ...task, ...patch } : task)) })
  // the task shape follows the ID while it is still the template, so that a
  // typed ID never leaves the shape targeting the old one
  const setTaskID = (task: TaskDraft, id: string) => {
    const shapes =
      task.shapes.taskShape === taskShapeTemplate(task.id) ? { ...task.shapes, taskShape: taskShapeTemplate(id) } : task.shapes
    updateTask(task.key, { id, shapes })
  }

  const checked = checkedText === assembled.text
  const problemCount = assembled.problems.length + serverProblems.length
  const ready = assembled.problems.length === 0 && checked && serverProblems.length === 0 && !checking
  const write = async () => {
    setWriting(true)
    setWriteError(null)
    try {
      await writeOntology(assembled.text)
      // the status poll notices the file and swaps the form for the notice
    } catch (caught) {
      setWriteError(caught instanceof Error ? caught.message : String(caught))
    } finally {
      setWriting(false)
    }
  }

  return (
    <div className="tab-body init">
      <form
        className="init-form"
        onSubmit={(event) => {
          event.preventDefault()
          if (ready && !writing) void write()
        }}
      >
        <section className="panel">
          <header className="panel-header">
            <h3>Module</h3>
            <p>
              The <code>owl:Ontology</code> node naming the module.
            </p>
          </header>
          <div className="field-grid">
            <Field id="title" label="Title" code="dcterms:title" help="The module's name, shown to a project browsing modules. Defaults to the git repository name.">
              <input id="title" value={draft.title} onChange={(event) => update({ title: event.target.value })} />
            </Field>
            <Field id="creator" label="Creator" code="dcterms:creator" help="Who maintains the module. Defaults to git user.name.">
              <input id="creator" value={draft.creator} onChange={(event) => update({ creator: event.target.value })} />
            </Field>
            <Field
              id="version"
              label="Version"
              code="owl:versionInfo"
              help="Free text such as 1.0. sal pins a module by its git commit, so this is for readers of the ontology rather than for sal."
            >
              <input id="version" placeholder="1.0" value={draft.version} onChange={(event) => update({ version: event.target.value })} />
            </Field>
            <Field
              id="comment"
              label="Description"
              code="rdfs:comment"
              wide
              help="A sentence on what the module is for. Optional: left out of the document when empty."
            >
              <textarea id="comment" value={draft.comment} onChange={(event) => update({ comment: event.target.value })} />
            </Field>
          </div>
        </section>

        <section className="panel">
          <header className="panel-header">
            <h3>Prefixes</h3>
            <HelpTip label="prefixes">
              The <code>@context</code> of <code>ontology.jsonld</code>. A shape that names a term from another vocabulary,{' '}
              <code>schema:name</code> say, needs that vocabulary's prefix bound here, or the term is written as a full
              IRI. The six base prefixes are always bound.
            </HelpTip>
            <p>Bound in every module ontology, plus the ones your shapes need.</p>
          </header>
          <div className="chips">
            {Object.entries(BASE_PREFIXES).map(([prefix, namespace]) => (
              <span key={prefix} className="badge" title={namespace}>
                {prefix}
              </span>
            ))}
          </div>
          <div className="prefix-rows">
            {draft.prefixes.map((entry, index) => (
              <div key={entry.key} className="prefix-row">
                <input
                  aria-label={`Prefix ${index + 1}`}
                  placeholder="schema"
                  spellCheck={false}
                  value={entry.prefix}
                  onChange={(event) => updatePrefix(entry.key, { prefix: event.target.value })}
                />
                <input
                  aria-label={`Namespace ${index + 1}`}
                  placeholder="https://schema.org/"
                  spellCheck={false}
                  value={entry.namespace}
                  onChange={(event) => updatePrefix(entry.key, { namespace: event.target.value })}
                />
                <button
                  type="button"
                  className="button small"
                  onClick={() => update({ prefixes: draft.prefixes.filter((other) => other.key !== entry.key) })}
                >
                  Remove
                </button>
              </div>
            ))}
          </div>
          <div className="panel-actions">
            <button type="button" className="button small" onClick={() => update({ prefixes: [...draft.prefixes, newPrefix()] })}>
              Add prefix
            </button>
          </div>
        </section>

        <section className="panel">
          <header className="panel-header">
            <h3>Tasks</h3>
            <HelpTip label="tasks">
              Each task is an <code>owl:Class</code> that subclasses <code>salmodule:Task</code>. A project runs one by
              writing an instance typed with it, and the container is handed that instance when it is run. A module
              declares at least one.
            </HelpTip>
            <p>What a project can ask the module to run.</p>
          </header>
          {draft.tasks.map((task, index) => (
            <TaskCard
              key={task.key}
              task={task}
              index={index}
              removable={draft.tasks.length > 1}
              onID={(id) => setTaskID(task, id)}
              onChange={(patch) => updateTask(task.key, patch)}
              onRemove={() => update({ tasks: draft.tasks.filter((other) => other.key !== task.key) })}
            />
          ))}
          <div className="panel-actions">
            <button type="button" className="button small" onClick={() => update({ tasks: [...draft.tasks, newTask()] })}>
              Add task
            </button>
          </div>
        </section>
      </form>

      <section className="panel init-preview">
        <header className="panel-header">
          <h3>ontology.jsonld</h3>
          <HelpTip label="the preview">
            Exactly what will be written to <code>{status.ontologyPath}</code>, checked as you type with the same rules{' '}
            <code>sal validate --salmodule</code> applies: the SAL module JSON Schema, that every term of the module's
            own namespace is declared, and that every shape is one sal can enforce. A problem is marked on the line it is
            about.
          </HelpTip>
          {checking && <span className="init-status hint">Checking…</span>}
          {!checking && problemCount > 0 && (
            <span className="init-status hint">
              {problemCount} {problemCount === 1 ? 'problem' : 'problems'}
            </span>
          )}
          {!checking && ready && <span className="init-status ok">Ready to write</span>}
        </header>
        {checkError && <p className="error-banner">Could not check the document: {checkError}</p>}
        {problemCount > 0 && (
          <ul className="problems">
            {assembled.problems.map((problem) => (
              <li key={problem} className="problem">
                {problem}
              </li>
            ))}
            {serverProblems.map((problem) => (
              <li key={`${problem.line}:${problem.message}`} className="problem">
                {problem.line > 0 && <span className="problem-line">line {problem.line}</span>}
                {problem.message}
              </li>
            ))}
          </ul>
        )}
        <Preview text={assembled.text} problems={serverProblems} />
        <div className="init-actions">
          <button type="button" className="button primary" disabled={!ready || writing} onClick={() => void write()}>
            {writing ? 'Writing…' : 'Write ontology.jsonld'}
          </button>
          {writeError ? (
            <p className="error-banner">{writeError}</p>
          ) : (
            <span className="hint">
              Also writes the <code>Dockerfile</code>, <code>.dockerignore</code>, and editor schema, leaving any that exist
              alone.
            </span>
          )}
        </div>
      </section>
    </div>
  )
}

function Field({
  id,
  label,
  code,
  help,
  wide,
  children,
}: {
  id: string
  label: string
  code: string
  help: string
  wide?: boolean
  children: React.ReactNode
}) {
  return (
    <div className={wide ? 'field wide' : 'field'}>
      <label htmlFor={id}>
        {label} <code>{code}</code>
        <HelpTip label={label.toLowerCase()}>{help}</HelpTip>
      </label>
      {children}
    </div>
  )
}

function TaskCard({
  task,
  index,
  removable,
  onID,
  onChange,
  onRemove,
}: {
  task: TaskDraft
  index: number
  removable: boolean
  onID: (id: string) => void
  onChange: (patch: Partial<TaskDraft>) => void
  onRemove: () => void
}) {
  const prefix = `task-${task.key}`
  return (
    <div className="task-card">
      <div className="task-head">
        <h4>
          Task {index + 1}
          {task.id.trim() && (
            <>
              {' '}
              <code>{task.id.trim()}</code>
            </>
          )}
        </h4>
        {removable && (
          <button type="button" className="button small" onClick={onRemove}>
            Remove task
          </button>
        )}
      </div>
      <div className="field-grid">
        <Field
          id={`${prefix}-id`}
          label="ID"
          code="@id"
          help="The class name a project types an instance with, and the last segment of its IRI under the module namespace. A bare name: letters, digits, and underscores, starting with a letter, such as FetchStations."
        >
          <input
            id={`${prefix}-id`}
            className="mono"
            placeholder="FetchStations"
            spellCheck={false}
            value={task.id}
            onChange={(event) => onID(event.target.value)}
          />
        </Field>
        <Field id={`${prefix}-label`} label="Label" code="rdfs:label" help="The human readable name of the task, which sal get classes shows.">
          <input
            id={`${prefix}-label`}
            placeholder="Fetch weather stations"
            value={task.label}
            onChange={(event) => onChange({ label: event.target.value })}
          />
        </Field>
        <Field
          id={`${prefix}-comment`}
          label="Comment"
          code="rdfs:comment"
          wide
          help="What the task does and what it produces, shown wherever the class is described."
        >
          <textarea
            id={`${prefix}-comment`}
            placeholder="Fetches every station in a region and emits each as a schema:Place"
            value={task.comment}
            onChange={(event) => onChange({ comment: event.target.value })}
          />
        </Field>
      </div>
      {SHAPE_KINDS.map((kind) => (
        <div key={kind} className="shape-section">
          <label htmlFor={`${prefix}-${kind}`}>
            {SHAPE_HELP[kind].title} <code>salmodule:{kind}</code>
            <HelpTip label={`the ${SHAPE_HELP[kind].title.toLowerCase()}`}>{SHAPE_HELP[kind].help}</HelpTip>
          </label>
          <ShapeEditor
            id={`${prefix}-${kind}`}
            value={task.shapes[kind]}
            example={shapeExample(kind, task.id.trim())}
            onChange={(value) => onChange({ shapes: { ...task.shapes, [kind]: value } })}
          />
        </div>
      ))}
    </div>
  )
}
