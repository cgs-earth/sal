# sal-ui

The web UI that `sal serve --with-ui` serves at `/`. It is a React + TypeScript app built
with Vite and styled in the Center for Geospatial Solutions brand palette, in a light and a
dark theme. The toggle in the header switches between them; the choice is kept in
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

## Development

Run the Go server in one terminal and Vite in another. Vite proxies `/api`, `/sparql`, and
`/geometries` to port 8080, so the UI talks to real data with hot reloading:

```sh
sal serve --with-ui   # terminal 1
make ui_dev           # terminal 2, then open the URL Vite prints
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

The SPARQL tab is `lazy()`-loaded because YASGUI and its bundled CodeMirror 5 account for
most of the build; keeping it out of the initial chunk halves what the app parses on load.

## Map tab

The Map tab is a placeholder. `GET /geometries` already returns the table's
`object_geometry` column as GeoJSON, but no MapLibre client is wired to it yet.
