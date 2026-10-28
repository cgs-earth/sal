import { useEffect, useMemo, useState } from 'react'
import { validateOntology, writeOntology, type ModuleStatus, type Problem } from './api'
import {
  assemble,
  BASE_PREFIXES,
  DEFAULT_VERSION,
  newDraft,
  newPrefix,
  newTask,
  parseDocument,
  SHAPE_KINDS,
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
    title: 'Configuration',
    help: 'The properties a project configures an instance of the task with, as a SHACL node shape (salmodule:taskShape) targeting the task class. Each row is one property: its name becomes a term of the module and the key the task instance carries it under, and the value kind, required, and single boxes become its datatype and cardinality. sal validates an instance against it before running the task.',
  },
  stdoutShape: {
    title: 'Output',
    help: 'The shape every node the task prints must satisfy (salmodule:stdoutShape). sal checks each line of output against it as the task runs and fails the build on the first violation; a task without one runs unchecked, with a warning. Set the target class to the @type the nodes carry, and constrain the properties with prefixed terms the prefixes bind. sh:languageIn, sh:uniqueLang, sh:disjoint, sh:qualifiedValueShapesDisjoint, and sh:sparql are refused.',
  },
  stdinShape: {
    title: 'Input',
    help: 'The shape of the nodes the task reads on standard input (salmodule:stdinShape), for a task that consumes what another task produced. Leave it undeclared for a task that only produces.',
  },
}

/**
 * The form that drafts a module ontology and writes it. It starts from the
 * ontology.jsonld the module has, or from a blank draft when it has none,
 * and the draft is assembled into the ontology.jsonld text on every edit;
 * what the form can tell is wrong is listed at once, and otherwise the text
 * goes to the server for the checks that need the SAL Module specification.
 * Write is offered only for a text the server checked and found nothing
 * wrong with, and writes over the file the form loaded.
 */
export function ModuleForm({ status }: { status: ModuleStatus }) {
  // the text the form loaded, which a write must replace and no other; after
  // a write it is the text written, so the next write builds on that
  const [base, setBase] = useState(status.document)
  const [unreadable, setUnreadable] = useState(false)
  const [draft, setDraft] = useState<ModuleDraft>(() => {
    if (!status.document) return newDraft(status.title, status.creator)
    const parsed = parseDocument(status.document)
    if (parsed) return parsed
    setUnreadable(true)
    return newDraft(status.title, status.creator)
  })
  const assembled = useMemo(() => assemble(draft), [draft])
  const [serverProblems, setServerProblems] = useState<Problem[]>([])
  // the text serverProblems are about, so a text edited since is not written
  const [checkedText, setCheckedText] = useState<string | null>(null)
  const [checking, setChecking] = useState(false)
  const [checkError, setCheckError] = useState<string | null>(null)
  const [writing, setWriting] = useState(false)
  const [writeError, setWriteError] = useState<string | null>(null)
  const [written, setWritten] = useState<string | null>(null)

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

  const checked = checkedText === assembled.text
  const problemCount = assembled.problems.length + serverProblems.length
  const ready = assembled.problems.length === 0 && checked && serverProblems.length === 0 && !checking
  // the file on disk is neither what was loaded nor what was last written,
  // so it was edited by hand since; the server refuses the write too
  const changedOnDisk = status.initialized && status.document !== base
  // what the file says, rendered the way the form renders it, so that a
  // file written by hand in another layout does not count as edited until
  // it is; saving an unedited one would only reformat it
  const baseText = useMemo(() => {
    if (!base) return base
    const parsed = parseDocument(base)
    return parsed ? assemble(parsed).text : base
  }, [base])
  const dirty = assembled.text !== baseText
  const write = async () => {
    setWriting(true)
    setWriteError(null)
    try {
      await writeOntology(assembled.text, base)
      setBase(assembled.text)
      setWritten(assembled.text)
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
          if (ready && !writing && !changedOnDisk) void write()
        }}
      >
        {unreadable && (
          <p className="error-banner">
            <code>{status.ontologyPath}</code> could not be read as a module ontology, so the form starts blank; writing
            replaces the file. To keep what it holds, edit it in your editor instead and check it with{' '}
            <code>sal validate --salmodule</code>.
          </p>
        )}
        {changedOnDisk && (
          <p className="error-banner">
            <code>{status.ontologyPath}</code> changed on disk since this page loaded it. Reload the page to edit the
            current file; writing from here is refused so those changes are not lost.
          </p>
        )}
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
              help="Free text such as 0.1, raised when the module's tasks or shapes change in a way a project would notice. sal pins a module by its git commit, so this is for readers of the ontology rather than for sal. Required."
            >
              <input id="version" placeholder={DEFAULT_VERSION} value={draft.version} onChange={(event) => update({ version: event.target.value })} />
            </Field>
            <Field
              id="comment"
              label="Description"
              code="rdfs:comment"
              wide
              help="A sentence on what the module is for and what its tasks produce, shown wherever the module is described. Required."
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
              <code>schema:name</code> say, needs that vocabulary's prefix bound here (<code>schema</code> to{' '}
              <code>https://schema.org/</code>), or the term is written as a full IRI. The six base prefixes are always bound, and every key a task prints is resolved through these.
            </HelpTip>
            <p>Bound in every module ontology, plus the ones your shapes and your task's output use.</p>
          </header>
          <div className="chips">
            {Object.entries(BASE_PREFIXES).map(([prefix, namespace]) => (
              <span key={prefix} className="badge" title={namespace}>
                {prefix}
              </span>
            ))}
            {Object.keys(draft.contextExtra).map((term) => (
              <span key={term} className="badge" title="A term definition the document holds, kept as it is">
                {term}
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
              onChange={(patch) => updateTask(task.key, patch)}
              onRemove={() => update({ tasks: draft.tasks.filter((other) => other.key !== task.key) })}
            />
          ))}
          <div className="panel-actions">
            <button type="button" className="button small" onClick={() => update({ tasks: [...draft.tasks, newTask()] })}>
              Add task
            </button>
          </div>
          {draft.otherNodes.length > 0 && (
            <p className="hint panel-note">
              {draft.otherNodes.length} other {draft.otherNodes.length === 1 ? 'node' : 'nodes'} in the graph, such as a property
              declared on its own, {draft.otherNodes.length === 1 ? 'is' : 'are'} kept as written; see the preview.
            </p>
          )}
        </section>
      </form>

      <div className="init-side">
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
            {!checking && ready && dirty && <span className="init-status ok">Ready to write</span>}
            {!checking && ready && !dirty && status.initialized && <span className="init-status ok">Written</span>}
          </header>
          {checkError && <p className="error-banner">Could not check the document: {checkError}</p>}
          <Preview text={assembled.text} problems={serverProblems} />
          <div className="init-actions">
            <button type="button" className="button primary" disabled={!ready || writing || !dirty || changedOnDisk} onClick={() => void write()}>
              {writing ? 'Writing…' : status.initialized || written ? 'Save ontology.jsonld' : 'Write ontology.jsonld'}
            </button>
            {writeError ? (
              <p className="error-banner">{writeError}</p>
            ) : written && !dirty ? (
              <span className="hint">
                Written to <code>{status.ontologyPath}</code>.{!status.dockerfile && ' Next: the Dockerfile, so the container answers salmodule ontology and salmodule run.'}
              </span>
            ) : (
              <span className="hint">
                Also writes the <code>Dockerfile</code>, <code>.dockerignore</code>, and editor schema where they are missing,
                leaving any that exist alone.
              </span>
            )}
            <Blockers
              problems={assembled.problems}
              serverProblems={serverProblems}
              checking={checking}
              dirty={dirty}
              changedOnDisk={changedOnDisk}
              initialized={status.initialized || written !== null}
            />
          </div>
        </section>
      </div>
    </div>
  )
}

/**
 * What stands between the form and a write, under the greyed out button,
 * folded up by default: the problems the form found, the ones sal found with
 * their lines, and the states that are not problems but still stop a write.
 */
function Blockers({
  problems,
  serverProblems,
  checking,
  dirty,
  changedOnDisk,
  initialized,
}: {
  problems: string[]
  serverProblems: Problem[]
  checking: boolean
  dirty: boolean
  changedOnDisk: boolean
  initialized: boolean
}) {
  const items: { key: string; line?: number; text: string }[] = [
    ...problems.map((text) => ({ key: text, text })),
    ...serverProblems.map((problem) => ({ key: `${problem.line}:${problem.message}`, line: problem.line, text: problem.message })),
  ]
  if (changedOnDisk) items.push({ key: 'disk', text: 'ontology.jsonld changed on disk since this page loaded it; reload the page.' })
  if (items.length === 0 && checking) items.push({ key: 'checking', text: 'sal is checking the document.' })
  if (items.length === 0 && !dirty) items.push({ key: 'clean', text: initialized ? 'Nothing to write: the file already holds this document.' : 'Nothing to write yet.' })
  if (items.length === 0) return null
  const count = problems.length + serverProblems.length
  return (
    <details className="blockers">
      <summary>
        {count > 0
          ? `${count} ${count === 1 ? 'thing' : 'things'} to fix or fill in before it can be written`
          : 'Why the button is disabled'}
      </summary>
      <ul>
        {items.map((item) => (
          <li key={item.key} className="problem">
            {item.line !== undefined && item.line > 0 && <span className="problem-line">line {item.line}</span>}
            {item.text}
          </li>
        ))}
      </ul>
    </details>
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
  onChange,
  onRemove,
}: {
  task: TaskDraft
  index: number
  removable: boolean
  onChange: (patch: Partial<TaskDraft>) => void
  onRemove: () => void
}) {
  const prefix = `task-${task.key}`
  const extraKeys = Object.keys(task.extra)
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
            onChange={(event) => onChange({ id: event.target.value })}
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
            placeholder="Fetches every station in a region and emits each as a dcterms:Location"
            value={task.comment}
            onChange={(event) => onChange({ comment: event.target.value })}
          />
        </Field>
      </div>
      {SHAPE_KINDS.map((kind) => (
        <div key={kind} className="shape-section">
          <label htmlFor={`${prefix}-${kind}`}>
            {SHAPE_HELP[kind].title} <code>salmodule:{kind}</code>
            <HelpTip label={`the ${SHAPE_HELP[kind].title.toLowerCase()} shape`}>{SHAPE_HELP[kind].help}</HelpTip>
          </label>
          <ShapeEditor
            id={`${prefix}-${kind}`}
            kind={kind}
            taskID={task.id.trim()}
            shape={task.shapes[kind]}
            onChange={(shape) => onChange({ shapes: { ...task.shapes, [kind]: shape } })}
          />
        </div>
      ))}
      {extraKeys.length > 0 && (
        <p className="hint shape-section">
          Also on this class, kept as written: <code>{extraKeys.join(', ')}</code>.
        </p>
      )}
    </div>
  )
}
