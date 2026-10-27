import { fileURLToPath } from 'node:url'
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
    rollupOptions: {
      // Two pages share one build: the app `sal serve --with-ui` serves, and
      // the module init form `sal init --salmodule --ui` serves. serve/ui.go
      // picks the page; the chunks they share are built once.
      input: {
        main: fileURLToPath(new URL('./index.html', import.meta.url)),
        init: fileURLToPath(new URL('./salmodule-init.html', import.meta.url)),
      },
    },
  },
  // `npm run dev` proxies the API to a `sal serve --with-ui` running on port
  // 8080, or to whatever SAL_UI_API names, such as the address a
  // `sal init --salmodule --ui` printed when working on /salmodule-init.html.
  // Everything the UI fetches from its own origin is listed, so the STAC links
  // and the Blobs tab behave as they do against the embedded build.
  server: {
    // Fixed so that `make dev` can say which URL to open; the proxied Go server
    // on 8080 serves the committed dist, not these sources.
    port: 5173,
    strictPort: true,
    proxy: {
      '/api': process.env.SAL_UI_API ?? 'http://localhost:8080',
      '/sparql': 'http://localhost:8080',
      '/geometries': 'http://localhost:8080',
      '/blobs': 'http://localhost:8080',
      '/stac': 'http://localhost:8080',
    },
  },
})
