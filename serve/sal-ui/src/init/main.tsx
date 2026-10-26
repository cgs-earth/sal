import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import '../index.css'
import { InitApp } from './InitApp.tsx'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <InitApp />
  </StrictMode>,
)
