/** Serializes a result set to CSV so it can be copied to the clipboard. */
export function toCSV(header: string[] | null, rows: string[][] | null): string {
  const escape = (value: string) => (/[",\n]/.test(value) ? `"${value.replaceAll('"', '""')}"` : value)
  const lines = [...(header ? [header] : []), ...(rows ?? [])]
  return lines.map((line) => line.map((cell) => escape(cell ?? '')).join(',')).join('\n')
}
