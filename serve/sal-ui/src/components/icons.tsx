/** The small line icons the copy buttons share, drawn in the current colour. */

export function ClipboardIcon() {
  return (
    <svg
      viewBox="0 0 16 16"
      width="14"
      height="14"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      aria-hidden="true"
    >
      <rect x="5.25" y="1.75" width="5.5" height="2.5" rx="0.75" />
      <path d="M10.75 3h1.5c.55 0 1 .45 1 1v9.25c0 .55-.45 1-1 1H3.75c-.55 0-1-.45-1-1V4c0-.55.45-1 1-1h1.5" />
    </svg>
  )
}

export function CheckIcon() {
  return (
    <svg
      viewBox="0 0 16 16"
      width="14"
      height="14"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M3.5 8.5 6.5 11.5 12.5 4.5" />
    </svg>
  )
}
