import { defineConfig } from 'vite'
import react, { reactCompilerPreset } from '@vitejs/plugin-react'
import babel from '@rolldown/plugin-babel'

// The build output is committed and embedded into the sal binary by serve/ui.go,
// so assets are always referenced from the server root.
export default defineConfig({
  base: '/',
  plugins: [react(), babel({ presets: [reactCompilerPreset()] })],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  // `npm run dev` proxies the API to a `sal serve --with-ui` running on port 8080.
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/sparql': 'http://localhost:8080',
      '/geometries': 'http://localhost:8080',
    },
  },
})
