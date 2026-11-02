import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import { App } from './App.tsx'
import { redirectToJSON } from './routing'

// A URL of the OGC API Features endpoint that asks for JSON with f=json is
// the server's to answer, never this app's; a proxy that hands every
// navigation to the app is sent back to the server for it.
if (!redirectToJSON(window.location)) {
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <App />
    </StrictMode>,
  )
}
