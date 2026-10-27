# sal-ui

The web UI that `sal serve --with-ui` serves at `/`, and the module init form that
`sal init --salmodule --ui` serves. It is a React + TypeScript app built with Vite and styled
in the Center for Geospatial Solutions brand palette, in a light and a dark theme. The toggle in the header switches between them; the choice is kept in
`localStorage` under `sal-ui.theme`, and an inline script in `index.html` applies it before
the first paint so a remembered theme never flashes. With nothing remembered the system
preference decides.

## How it reaches the binary

`serve/ui.go` embeds `dist/` with `//go:embed`, so **`dist/` is committed to Git**. A
`go install` of SAL therefore ships the UI with no Node toolchain involved. The tradeoff is
that the build output has to be regenerated and committed whenever the sources change:

```sh
make ui   # from the repository root: npm ci && npm run build
```

`.github/workflows/ui_build.yml` rebuilds the app in CI and fails if the committed `dist/`
does not match the sources, so a stale UI cannot ship silently.

## Two pages, one build

`index.html` is the app `sal serve --with-ui` serves, and `salmodule-init.html` is the form
`sal init --salmodule --ui` serves. Both are entries of the one Vite build (`vite.config.ts`),
so they share the theme, the components, and the chunks, and `serve/ui.go` picks which page a
server falls back to. The form's sources are under `src/init/`; it talks to the three
`/api/init/*` endpoints the `initialization` package answers.

## Development

One target runs the Go server and Vite together. Vite proxies `/api`, `/sparql`, `/geometries`,
`/blobs`, and `/stac` to port 8080, so the UI talks to real data with hot reloading:

```sh
make dev              # then open http://localhost:5173
```

Open Vite's port, not 8080: the Go server on 8080 serves the committed `dist` embedded in
the binary, so an edit under `src` only shows on 5173 until `make ui` rebuilds `dist`.

That runs both halves with hot reload: Vite serves the UI from `src` with hot module
replacement, and [air](https://github.com/air-verse/air), fetched on demand with `go run`,
rebuilds sal into `.tmp/` and restarts `sal serve --with-ui` on every Go change. The server
reads the SAL project in the current directory; `SAL_DEV_PROJECT=path make dev` points it
at another project's table.

For the module init form, run `sal init --salmodule --ui` in a module repository instead,
point the proxy at the address it prints, and open `/salmodule-init.html` on Vite's port:

```sh
SAL_UI_API=http://127.0.0.1:PORT npm run dev
```

## Layout

| Path                    | Purpose                                                     |
| ----------------------- | ----------------------------------------------------------- |
| `src/App.tsx`           | Shell and tab switching                                     |
| `src/api.ts`            | Typed wrappers over the Go JSON endpoints                    |
| `src/theme.ts`          | Theme switching, the canvas palettes, and the CodeMirror 6 theme |
| `src/index.css`         | CGS brand constants, the light and dark token sets, base styles |
| `src/App.css`           | App layout, panels, and result tables                        |
| `src/yasgui-theme.css`  | Repaints YASGUI's fixed light theme in the page's tokens     |
| `src/tabs/`             | One file per tab: Stats, SQL, SPARQL, Map                   |
| `src/init/`             | The module init form: its entry, API, document assembly, and editors |

The SPARQL tab is `lazy()`-loaded because YASGUI and its bundled CodeMirror 5 account for
most of the build; keeping it out of the initial chunk halves what the app parses on load.

## Map tab

The Map tab is a placeholder. `GET /geometries` already returns the table's
`object_geometry` column as GeoJSON, but no MapLibre client is wired to it yet.
