import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { EditorView } from '@codemirror/view'
import { tags } from '@lezer/highlight'

/** Catppuccin Mocha, matching the palette the `sal query --sparql` shell uses. */
export const mocha = {
  rosewater: '#f5e0dc',
  flamingo: '#f2cdcd',
  pink: '#f5c2e7',
  mauve: '#cba6f7',
  red: '#f38ba8',
  maroon: '#eba0ac',
  peach: '#fab387',
  yellow: '#f9e2af',
  green: '#a6e3a1',
  teal: '#94e2d5',
  sky: '#89dceb',
  sapphire: '#74c7ec',
  blue: '#89b4fa',
  lavender: '#b4befe',
  text: '#cdd6f4',
  subtext1: '#bac2de',
  subtext0: '#a6adc8',
  overlay2: '#9399b2',
  overlay1: '#7f849c',
  overlay0: '#6c7086',
  surface2: '#585b70',
  surface1: '#45475a',
  surface0: '#313244',
  base: '#1e1e2e',
  mantle: '#181825',
  crust: '#11111b',
} as const

const highlightStyle = HighlightStyle.define([
  { tag: tags.keyword, color: mocha.blue, fontWeight: '600' },
  { tag: [tags.operatorKeyword, tags.modifier], color: mocha.mauve },
  { tag: [tags.string, tags.special(tags.string)], color: mocha.green },
  { tag: [tags.number, tags.bool, tags.null], color: mocha.peach },
  { tag: [tags.comment, tags.lineComment, tags.blockComment], color: mocha.overlay1, fontStyle: 'italic' },
  { tag: [tags.function(tags.variableName), tags.function(tags.propertyName)], color: mocha.sapphire },
  { tag: [tags.typeName, tags.className], color: mocha.yellow },
  { tag: [tags.propertyName, tags.attributeName], color: mocha.teal },
  { tag: [tags.variableName, tags.name], color: mocha.text },
  { tag: [tags.operator, tags.punctuation, tags.bracket], color: mocha.overlay2 },
  { tag: tags.invalid, color: mocha.red },
])

const editorTheme = EditorView.theme(
  {
    '&': {
      color: mocha.text,
      backgroundColor: mocha.mantle,
      fontSize: '13px',
      height: '100%',
    },
    '.cm-content': {
      caretColor: mocha.lavender,
      fontFamily: 'var(--font-mono)',
      padding: '12px 0',
    },
    '.cm-scroller': {
      backgroundColor: mocha.mantle,
      fontFamily: 'var(--font-mono)',
      lineHeight: '1.6',
    },
    '&.cm-focused': { outline: 'none' },
    '.cm-cursor, .cm-dropCursor': { borderLeftColor: mocha.lavender, borderLeftWidth: '2px' },
    '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': {
      backgroundColor: mocha.surface2,
    },
    '.cm-activeLine': { backgroundColor: '#ffffff08' },
    '.cm-gutters': {
      backgroundColor: mocha.mantle,
      color: mocha.overlay0,
      border: 'none',
      borderRight: `1px solid ${mocha.surface0}`,
    },
    '.cm-activeLineGutter': { backgroundColor: '#ffffff08', color: mocha.lavender },
    '.cm-selectionMatch': { backgroundColor: mocha.surface1 },
    '.cm-matchingBracket, &.cm-focused .cm-matchingBracket': {
      backgroundColor: mocha.surface1,
      color: mocha.lavender,
      outline: `1px solid ${mocha.lavender}`,
    },
    '.cm-tooltip': {
      backgroundColor: mocha.surface0,
      border: `1px solid ${mocha.surface2}`,
      borderRadius: '6px',
      color: mocha.text,
    },
    '.cm-tooltip-autocomplete > ul > li[aria-selected]': {
      backgroundColor: mocha.lavender,
      color: mocha.crust,
    },
  },
  { dark: true },
)

/** The CodeMirror extension bundle shared by every editor in the app. */
export const catppuccin = [editorTheme, syntaxHighlighting(highlightStyle)]
