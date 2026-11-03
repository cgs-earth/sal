import { useEffect, useId, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'
import type { Vocabulary } from '../../api'
import {
  ask,
  DEFAULT_MODEL,
  MODELS_URL,
  readSettings,
  resetChat,
  stopAssistant,
  useChat,
  writeSettings,
  type AssistantSettings,
  type ChatMessage,
  type ToolPart,
} from './assistant'
import { Markdown } from './Markdown'

type AssistantPaneProps = {
  /** The vocabularies the project pins, as the Prefixes dialog lists them, or null while they are still being read. */
  vocabularies: Vocabulary[] | null
  /** The table's snapshot IDs, newest first. */
  snapshots: string[]
  /** The query the editor holds, read when a message is sent. */
  currentQuery: () => string
  /** Puts a query the assistant wrote into the editor. */
  onInsert: (query: string) => void
  /** Puts it into the editor and runs it. */
  onRun: (query: string) => void
  onClose: () => void
}

/** Questions to start from, offered while the conversation is empty. */
const SUGGESTIONS = [
  'What kinds of things are in this dataset?',
  'Write a query listing every subject with its label and type.',
  'Which predicates are used most, and on what?',
  'Explain the query in the editor and suggest how to narrow it.',
]

/**
 * The chat docked beside the SPARQL editor: a message list, a box to type in,
 * and the settings the assistant needs, an OpenRouter API key and a model.
 * The key is asked for before anything else, and kept in the browser. Every
 * query the assistant answers with gets Insert and Run buttons that put it
 * into the editor.
 */
export function AssistantPane({ vocabularies, snapshots, currentQuery, onInsert, onRun, onClose }: AssistantPaneProps) {
  const chat = useChat()
  const [settings, setSettings] = useState<AssistantSettings>(readSettings)
  const [showSettings, setShowSettings] = useState(() => !settings.apiKey)
  const [draft, setDraft] = useState('')
  const messagesEl = useRef<HTMLDivElement>(null)
  const inputEl = useRef<HTMLTextAreaElement>(null)

  // The list follows the newest message as it streams in.
  useEffect(() => {
    const el = messagesEl.current
    if (el) el.scrollTop = el.scrollHeight
  }, [chat])

  const send = (text: string) => {
    const message = text.trim()
    if (!message || chat.busy || !settings.apiKey) return
    setDraft('')
    void ask(message, {
      settings,
      vocabularies: vocabularies ?? [],
      snapshots,
      editorQuery: currentQuery(),
    })
  }

  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault()
      send(draft)
    }
  }

  return (
    <aside className="assistant-pane" aria-label="SPARQL assistant">
      <header className="assistant-header">
        <h4>Assistant</h4>
        <span className="hint assistant-model" title="The OpenRouter model answering">
          {settings.model || DEFAULT_MODEL}
        </span>
        <div className="assistant-header-actions">
          <button
            type="button"
            className={`button small${showSettings ? ' active' : ''}`}
            aria-expanded={showSettings}
            title="The OpenRouter API key and model"
            onClick={() => setShowSettings((open) => !open)}
          >
            Settings
          </button>
          <button
            type="button"
            className="button small"
            disabled={chat.messages.length === 0}
            title="Forget the conversation and start another"
            onClick={resetChat}
          >
            New chat
          </button>
          <button type="button" className="icon-button" aria-label="Close the assistant" title="Close" onClick={onClose}>
            ×
          </button>
        </div>
      </header>
      {showSettings && (
        <SettingsForm
          settings={settings}
          onSave={(next) => {
            writeSettings(next)
            setSettings(next)
            setShowSettings(false)
            inputEl.current?.focus()
          }}
          onCancel={settings.apiKey ? () => setShowSettings(false) : null}
        />
      )}
      <div className="assistant-messages" ref={messagesEl}>
        {chat.messages.length === 0 && (
          <div className="assistant-welcome">
            <p>
              Ask for a query in plain words. The assistant knows the vocabularies this project pins, looks at the classes and
              predicates the table uses, and runs its drafts against <code>/sparql</code> before answering, so what it hands
              over has been tried.
            </p>
            <div className="assistant-suggestions">
              {SUGGESTIONS.map((suggestion) => (
                <button
                  key={suggestion}
                  type="button"
                  className="chip"
                  disabled={!settings.apiKey || chat.busy}
                  onClick={() => send(suggestion)}
                >
                  {suggestion}
                </button>
              ))}
            </div>
          </div>
        )}
        {chat.messages.map((message) => (
          <Message key={message.id} message={message} onInsert={onInsert} onRun={onRun} />
        ))}
      </div>
      <form
        className="assistant-compose"
        onSubmit={(event: FormEvent) => {
          event.preventDefault()
          send(draft)
        }}
      >
        <textarea
          ref={inputEl}
          className="module-input assistant-input"
          rows={2}
          placeholder={settings.apiKey ? 'Describe the query you want… (Enter to send, Shift+Enter for a new line)' : 'Enter an OpenRouter API key in the settings to start.'}
          value={draft}
          disabled={!settings.apiKey}
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={onKeyDown}
        />
        {chat.busy ? (
          <button type="button" className="button" onClick={stopAssistant} title="Stop the answer where it is">
            Stop
          </button>
        ) : (
          <button type="submit" className="button primary" disabled={!settings.apiKey || !draft.trim()}>
            Send
          </button>
        )}
      </form>
    </aside>
  )
}

function Message({
  message,
  onInsert,
  onRun,
}: {
  message: ChatMessage
  onInsert: (query: string) => void
  onRun: (query: string) => void
}) {
  if (message.role === 'user') {
    return (
      <div className="assistant-message user">
        <p>{message.text}</p>
      </div>
    )
  }
  const renderCode = (code: string, language: string) => {
    // A block the assistant tagged as anything else, SQL or JSON say, is only shown.
    const isQuery = language === '' || language === 'sparql'
    return (
      <div className="assistant-code">
        <pre>
          <code>{code}</code>
        </pre>
        {isQuery && (
          <div className="assistant-code-actions">
            <button type="button" className="button small" onClick={() => onInsert(code)} title="Put this query into the editor">
              Insert
            </button>
            <button type="button" className="button small primary" onClick={() => onRun(code)} title="Put this query into the editor and run it">
              Run
            </button>
          </div>
        )}
      </div>
    )
  }
  return (
    <div className={`assistant-message assistant ${message.status}`}>
      {message.parts.map((part, i) =>
        part.kind === 'text' ? <Markdown key={i} text={part.text} renderCode={renderCode} /> : <ToolRow key={part.id} part={part} />,
      )}
      {message.status === 'streaming' && <span className="assistant-thinking" aria-label="Working">…</span>}
      {message.status === 'stopped' && <p className="hint">Stopped.</p>}
      {message.status === 'failed' && <p className="error-banner assistant-error">{message.error}</p>}
    </div>
  )
}

/** One tool call as a row that opens on what was asked and what came back. */
function ToolRow({ part }: { part: ToolPart }) {
  return (
    <details className={`assistant-tool ${part.status}`}>
      <summary>
        <span className="assistant-tool-dot" aria-hidden="true" />
        <span className="assistant-tool-title">{part.title}</span>
        <span className="assistant-tool-summary">{part.summary}</span>
      </summary>
      <pre>{JSON.stringify({ input: part.input, output: part.output ?? null }, null, 2)}</pre>
    </details>
  )
}

function SettingsForm({
  settings,
  onSave,
  onCancel,
}: {
  settings: AssistantSettings
  onSave: (settings: AssistantSettings) => void
  /** Null while there is no key yet, since there is nothing to go back to. */
  onCancel: (() => void) | null
}) {
  const [apiKey, setApiKey] = useState(settings.apiKey)
  const [model, setModel] = useState(settings.model)
  const [models, setModels] = useState<string[]>([])
  const listId = useId()

  // The models OpenRouter serves, for completing the model box; a listing
  // that cannot be read leaves the box free text, which it is anyway.
  useEffect(() => {
    const controller = new AbortController()
    fetch(MODELS_URL, { signal: controller.signal })
      .then((response) => (response.ok ? response.json() : Promise.reject(new Error(response.statusText))))
      .then((listing: { data?: { id: string }[] }) => {
        const ids = (listing.data ?? []).map((entry) => entry.id).filter((id) => !id.endsWith(':batch'))
        setModels(ids.sort())
      })
      .catch(() => setModels([]))
    return () => controller.abort()
  }, [])

  return (
    <form
      className="assistant-settings"
      onSubmit={(event) => {
        event.preventDefault()
        onSave({ apiKey: apiKey.trim(), model: model.trim() || DEFAULT_MODEL })
      }}
    >
      <label>
        <span>OpenRouter API key</span>
        <input
          className="module-input"
          type="password"
          autoComplete="off"
          placeholder="sk-or-…"
          value={apiKey}
          onChange={(event) => setApiKey(event.target.value)}
        />
      </label>
      <label>
        <span>Model</span>
        <input
          className="module-input"
          type="text"
          list={listId}
          placeholder={DEFAULT_MODEL}
          value={model}
          onChange={(event) => setModel(event.target.value)}
        />
        <datalist id={listId}>
          {models.map((id) => (
            <option key={id} value={id} />
          ))}
        </datalist>
      </label>
      <p className="hint">
        The key stays in this browser&apos;s local storage and is sent to openrouter.ai and nowhere else. The model is sent
        your messages, the query in the editor, the names and comments of the pinned vocabularies, and the first rows of
        every query the assistant runs, so do not use it on a dataset that must not leave this machine. Get a key at{' '}
        <a href="https://openrouter.ai/keys" target="_blank" rel="noreferrer">
          openrouter.ai/keys
        </a>
        .
      </p>
      <div className="assistant-settings-actions">
        <button type="submit" className="button small primary" disabled={!apiKey.trim()}>
          Save
        </button>
        {settings.apiKey && (
          <button
            type="button"
            className="button small"
            title="Remove the key from this browser"
            onClick={() => onSave({ apiKey: '', model: model.trim() || DEFAULT_MODEL })}
          >
            Forget key
          </button>
        )}
        {onCancel && (
          <button type="button" className="button small" onClick={onCancel}>
            Cancel
          </button>
        )}
      </div>
    </form>
  )
}
