import { useTheme } from '../theme'

/**
 * The header button that switches between the light and the dark theme. It
 * shows the theme a click would switch to, a sun on the dark theme and a moon
 * on the light one, and the choice is remembered across visits by theme.ts.
 */
export function ThemeToggle() {
  const [theme, toggle] = useTheme()
  const next = theme === 'dark' ? 'light' : 'dark'
  const label = `Switch to the ${next} theme`
  return (
    <button type="button" className="icon-button theme-toggle" aria-label={label} title={label} onClick={toggle}>
      {theme === 'dark' ? (
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true">
          <circle cx="12" cy="12" r="4" />
          <path d="M12 2.5v2.5M12 19v2.5M2.5 12H5M19 12h2.5M5.3 5.3l1.8 1.8M16.9 16.9l1.8 1.8M5.3 18.7l1.8-1.8M16.9 7.1l1.8-1.8" />
        </svg>
      ) : (
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M20 14.5A8.5 8.5 0 0 1 9.5 4a8.5 8.5 0 1 0 10.5 10.5z" />
        </svg>
      )}
    </button>
  )
}
