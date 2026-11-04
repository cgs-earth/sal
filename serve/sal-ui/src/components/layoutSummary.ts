import type { ColumnPruning, DataFile, FileOutlier, TableLayout } from '../api'

export const formatCount = (value: number) => new Intl.NumberFormat().format(Math.round(value))

/** Bytes as the nearest unit, 24.7 MB, the way a file browser shows a size. */
export function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let value = bytes / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`
}

export const formatRatio = (ratio: number) => (ratio >= 10 ? `${Math.round(ratio)}×` : `${ratio.toFixed(1)}×`)

export const formatShare = (part: number, whole: number) => (whole > 0 ? `${Math.round((part / whole) * 100)}%` : '0%')

export const fileName = (file: DataFile) => file.path.slice(file.path.lastIndexOf('/') + 1)

export type Note = { text: string; tone: 'good' | 'warn' | 'plain' }

/** What stands out, each in a line: the files far larger or smaller than the rest and the deletes every scan pays for. */
export function layoutNotes(layout: TableLayout, outliers: FileOutlier[]): Note[] {
  const data = layout.dataFiles
  const notes: Note[] = []
  if (data.count === 0) {
    return [{ text: 'No data files', tone: 'plain' }]
  }
  const small = outliers.filter((outlier) => outlier.kind === 'small')
  const large = outliers.filter((outlier) => outlier.kind === 'large')
  if (large.length > 0) {
    const share = large.reduce((sum, outlier) => sum + outlier.file.rows, 0)
    notes.push({
      tone: 'warn',
      text: `${plural(large.length, 'file')} ${large.length === 1 ? 'holds' : 'hold'} ${formatShare(share, data.rows)} of the rows, the largest ${formatCount(large[0].file.rows)} (${formatRatio(large[0].ratio)} the average)`,
    })
  }
  if (small.length > 0) {
    const rows = small.reduce((sum, outlier) => sum + outlier.file.rows, 0)
    notes.push({
      tone: 'warn',
      text: `${plural(small.length, 'near-empty file')} ${small.length === 1 ? 'holds' : 'hold'} ${formatCount(rows)} rows between them`,
    })
  }
  if (layout.deleteFiles.count > 0) {
    notes.push({
      tone: layout.deleteFiles.rows > data.rows / 5 ? 'warn' : 'plain',
      text: `${plural(layout.deleteFiles.count, 'delete file')} subtract ${formatCount(layout.deleteFiles.rows)} triples (${formatShare(layout.deleteFiles.rows, data.rows)} of the live rows) from every scan`,
    })
  }
  return notes
}

const plural = (count: number, noun: string) => `${formatCount(count)} ${noun}${count === 1 ? '' : 's'}`

export type Verdict = { grade: 'good' | 'fair' | 'poor' | 'none'; tone: 'good' | 'warn' | 'plain'; detail: string }

/**
 * How well a filter on the column skips files, graded from how many files one
 * value can fall in by the bounds the data files record, with the reasoning
 * spelled out for the hover.
 */
export function pruningVerdict(column: ColumnPruning, dataFiles: number): Verdict {
  const files = (count: number) => `${count} of the ${dataFiles} data files`
  if (column.type.toUpperCase().startsWith('GEOMETRY')) {
    return {
      grade: 'none',
      tone: 'plain',
      detail: `Iceberg records a bounding box per file for a geometry, which DuckDB does not use to skip files, so a spatial filter reads every one of the ${dataFiles} data files.`,
    }
  }
  if (column.filesWithBounds === 0) {
    return {
      grade: 'none',
      tone: 'plain',
      detail: `No data file records a lowest and highest value for ${column.name}, so nothing lets a filter on it skip a file.`,
    }
  }
  if (column.filesWithBounds < dataFiles / 2) {
    return {
      grade: 'good',
      tone: 'good',
      detail: `Only ${files(column.filesWithBounds)} hold a value for ${column.name} at all, and a filter on it skips the rest outright; of those, a single value can fall in at most ${column.maxFilesPerValue}.`,
    }
  }
  const share = column.maxFilesPerValue / column.filesWithBounds
  if (column.maxFilesPerValue <= 2 || share <= 0.1) {
    return {
      grade: 'good',
      tone: 'good',
      detail: `The ranges the files record for ${column.name} barely overlap, so a filter for one value reads at most ${files(column.maxFilesPerValue)} and skips the rest by their bounds.`,
    }
  }
  if (share <= 0.5) {
    return {
      grade: 'fair',
      tone: 'plain',
      detail: `The ranges the files record for ${column.name} overlap in places: a filter for one value reads up to ${files(column.maxFilesPerValue)}, the rest being skipped by their bounds.`,
    }
  }
  return {
    grade: 'poor',
    tone: 'warn',
    detail: `The ranges the files record for ${column.name} overlap almost everywhere, so a filter for one value reads up to ${files(column.maxFilesPerValue)}; the bounds skip little, and partitioning or sorting by ${column.name} is what would change that.`,
  }
}
