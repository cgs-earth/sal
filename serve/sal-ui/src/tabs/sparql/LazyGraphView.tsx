import { lazy } from 'react'

// Isolated in its own file so GraphPlugin.tsx (a Yasr plugin adapter, not a component
// module) doesn't mix a component export with its class/function exports — oxlint's
// react-refresh rule flags that combination.
export const LazyGraphView = lazy(() => import('./GraphView').then((module) => ({ default: module.GraphView })))
