import { useCallback, useSyncExternalStore } from 'react'
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { EditorView } from '@codemirror/view'
import { tags } from '@lezer/highlight'

/*
 * The UI is styled in the Center for Geospatial Solutions palette, in a light
 * and a dark theme. Every color is a CSS custom property declared in index.css
 * for the theme `<html data-theme>` names, so the stylesheets and the CodeMirror
 * theme below never need to know which theme is on. The two places that paint
 * on a canvas, the Sigma graph and MapLibre's layers, cannot read a `var()`,
 * so they take their colors from the palettes below, keyed by the theme
 * `useTheme` reports.
 */

export type Theme = 'light' | 'dark'

/** The brand palette, as the CGS guidelines name it; index.css declares the same values. */
export const cgs = {
  continentalGreen: '#46ab9d',
  fluvialBlue: '#2c84b9',
  rayleighBlue: '#86c1e2',
  cumulusGray: '#c9dee8',
  prairieYellow: '#f9c609',
  cream: '#fefcf5',
  slateGray: '#82848b',
  pavementGray: '#48535b',
} as const

/** The literal colors the SPARQL graph is painted with under a theme. */
export interface GraphPalette {
  uri: string
  blank: string
  literal: string
  root: string
  edge: string
  label: string
  edgeLabel: string
  hoverBackground: string
  hoverShadow: string
}

export function graphPalette(theme: Theme): GraphPalette {
  return theme === 'dark'
    ? {
        uri: cgs.rayleighBlue,
        blank: cgs.slateGray,
        literal: cgs.continentalGreen,
        root: cgs.prairieYellow,
        edge: cgs.slateGray,
        label: cgs.cream,
        edgeLabel: cgs.cumulusGray,
        hoverBackground: '#232c32',
        hoverShadow: 'rgb(0 0 0 / 60%)',
      }
    : {
        uri: cgs.fluvialBlue,
        blank: cgs.slateGray,
        literal: cgs.continentalGreen,
        root: cgs.prairieYellow,
        edge: '#9aaab2',
        label: cgs.pavementGray,
        edgeLabel: '#5f6b73',
        hoverBackground: '#ffffff',
        hoverShadow: 'rgb(72 83 91 / 35%)',
      }
}

/** The literal colors the Map tab's layers are painted with under a theme. */
export interface MapPalette {
  feature: string
  point: string
  pointStroke: string
  box: string
  extent: string
}

export function mapPalette(theme: Theme): MapPalette {
  return theme === 'dark'
    ? { feature: cgs.rayleighBlue, point: cgs.prairieYellow, pointStroke: '#11161a', box: cgs.continentalGreen, extent: cgs.cumulusGray }
    : { feature: cgs.fluvialBlue, point: cgs.prairieYellow, pointStroke: cgs.pavementGray, box: cgs.continentalGreen, extent: cgs.slateGray }
}

/** Where the chosen theme is remembered between visits. */
const STORAGE_KEY = 'sal-ui.theme'

const listeners = new Set<() => void>()

/** The theme the page shows now, for code outside React such as a canvas draw function. */
export function currentTheme(): Theme {
  return document.documentElement.dataset.theme === 'light' ? 'light' : 'dark'
}

/**
 * Applies a theme to the page and remembers it, so the inline script in
 * index.html can restore it before the first paint on the next visit.
 */
export function setTheme(theme: Theme) {
  document.documentElement.dataset.theme = theme
  try {
    localStorage.setItem(STORAGE_KEY, theme)
  } catch {
    // Private windows may refuse storage; the theme still applies for this visit.
  }
  for (const listener of listeners) listener()
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

/** The current theme and a function that switches to the other one. */
export function useTheme(): [Theme, () => void] {
  const theme = useSyncExternalStore(subscribe, currentTheme, () => 'dark' as Theme)
  const toggle = useCallback(() => setTheme(currentTheme() === 'dark' ? 'light' : 'dark'), [])
  return [theme, toggle]
}

/*
 * The CodeMirror 6 theme shared by the SQL editor and the SPARQL tab's SQL
 * pane. It is written entirely in the page's custom properties, so a theme
 * switch repaints an open editor without rebuilding it; the same properties
 * color Yasgui's CodeMirror 5 in yasgui-theme.css so both editors match.
 */
const highlightStyle = HighlightStyle.define([
  { tag: tags.keyword, color: 'var(--syntax-keyword)', fontWeight: '600' },
  { tag: [tags.operatorKeyword, tags.modifier], color: 'var(--syntax-keyword)' },
  { tag: [tags.string, tags.special(tags.string)], color: 'var(--syntax-string)' },
  { tag: [tags.number, tags.bool, tags.null], color: 'var(--syntax-number)' },
  { tag: [tags.comment, tags.lineComment, tags.blockComment], color: 'var(--syntax-comment)', fontStyle: 'italic' },
  { tag: [tags.function(tags.variableName), tags.function(tags.propertyName)], color: 'var(--syntax-function)' },
  { tag: [tags.typeName, tags.className], color: 'var(--syntax-type)' },
  { tag: [tags.propertyName, tags.attributeName], color: 'var(--syntax-property)' },
  { tag: [tags.variableName, tags.name], color: 'var(--text)' },
  { tag: [tags.operator, tags.punctuation, tags.bracket], color: 'var(--syntax-punctuation)' },
  { tag: tags.invalid, color: 'var(--danger)' },
])

const editorTheme = EditorView.theme({
  '&': {
    color: 'var(--text)',
    backgroundColor: 'var(--bg-sunken)',
    fontSize: '13px',
    height: '100%',
  },
  '.cm-content': {
    caretColor: 'var(--accent)',
    fontFamily: 'var(--font-mono)',
    padding: '12px 0',
  },
  // A CodeMirror editor with a fixed height only scrolls if its scroller says
  // so; the base theme sets overflow-x alone, which clips longer statements.
  '.cm-scroller': {
    backgroundColor: 'var(--bg-sunken)',
    fontFamily: 'var(--font-mono)',
    lineHeight: '1.6',
    overflow: 'auto',
  },
  '&.cm-focused': { outline: 'none' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--accent)', borderLeftWidth: '2px' },
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': {
    backgroundColor: 'var(--selection)',
  },
  '.cm-activeLine': { backgroundColor: 'var(--line-highlight)' },
  '.cm-gutters': {
    backgroundColor: 'var(--bg-sunken)',
    color: 'var(--text-disabled)',
    border: 'none',
    borderRight: '1px solid var(--line)',
  },
  '.cm-activeLineGutter': { backgroundColor: 'var(--line-highlight)', color: 'var(--accent-ink)' },
  '.cm-selectionMatch': { backgroundColor: 'var(--selection)' },
  '.cm-matchingBracket, &.cm-focused .cm-matchingBracket': {
    backgroundColor: 'var(--bg-hover)',
    color: 'var(--accent-ink)',
    outline: '1px solid var(--accent)',
  },
  '.cm-tooltip': {
    backgroundColor: 'var(--bg-raised)',
    border: '1px solid var(--line-emphasis)',
    borderRadius: '6px',
    color: 'var(--text)',
  },
  '.cm-tooltip-autocomplete > ul > li[aria-selected]': {
    backgroundColor: 'var(--accent)',
    color: 'var(--accent-text)',
  },
})

/** The CodeMirror extension bundle shared by every editor in the app. */
export const editorExtensions = [editorTheme, syntaxHighlighting(highlightStyle)]
