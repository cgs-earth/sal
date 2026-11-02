import type { OgcFeature } from '../../api'

/** One property of a feature that differs between the two snapshots. */
export type PropertyChange = {
  key: string
  primary: unknown
  compare: unknown
}

export type ChangedFeature = {
  primary: OgcFeature
  compare: OgcFeature
  geometryChanged: boolean
  properties: PropertyChange[]
}

/** The features of two snapshots set against each other by id. */
export type FeatureDiff = {
  onlyPrimary: OgcFeature[]
  onlyCompare: OgcFeature[]
  changed: ChangedFeature[]
  unchanged: OgcFeature[]
}

function same(a: unknown, b: unknown): boolean {
  return JSON.stringify(a ?? null) === JSON.stringify(b ?? null)
}

/**
 * Sets the features of two snapshots against each other by id: the ones only
 * one snapshot has, the ones both have with a different geometry or property,
 * and the rest. Properties are compared key by key over the union of both
 * sides' keys, so a property one side lacks reads as changed from null. The
 * server computes nothing for this; it is two fetches compared here.
 */
export function diffFeatures(primary: OgcFeature[], compare: OgcFeature[]): FeatureDiff {
  const byId = new Map(compare.map((feature) => [feature.id, feature]))
  const diff: FeatureDiff = {
    onlyPrimary: [],
    onlyCompare: [],
    changed: [],
    unchanged: [],
  }
  for (const feature of primary) {
    const other = byId.get(feature.id)
    if (!other) {
      diff.onlyPrimary.push(feature)
      continue
    }
    byId.delete(feature.id)
    const keys = new Set([...Object.keys(feature.properties), ...Object.keys(other.properties)])
    const properties: PropertyChange[] = []
    for (const key of keys) {
      if (!same(feature.properties[key], other.properties[key])) {
        properties.push({
          key,
          primary: feature.properties[key],
          compare: other.properties[key],
        })
      }
    }
    const geometryChanged = !same(feature.geometry, other.geometry)
    if (geometryChanged || properties.length > 0) {
      diff.changed.push({
        primary: feature,
        compare: other,
        geometryChanged,
        properties,
      })
    } else {
      diff.unchanged.push(feature)
    }
  }
  diff.onlyCompare = [...byId.values()]
  return diff
}

/** A property value as a table cell: ∅ for null, text as it is, anything else as JSON. */
export function describeValue(value: unknown): string {
  if (value === null || value === undefined) return '∅'
  if (typeof value === 'string') return value
  return JSON.stringify(value)
}
