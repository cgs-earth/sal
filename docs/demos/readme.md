# Demo recordings

The terminal GIFs embedded in [the command reference](../src/content/docs/reference/commands.mdx),
recorded with [VHS](https://github.com/charmbracelet/vhs).

The GIFs are **not committed**. `.github/workflows/demos.yml` records them from the
working tree on every push and uploads them as the `demo-gifs` artifact;
`push_pages.yml` downloads that artifact into `docs/public/demos` before building the
site, so what the docs show is always what the current CLI prints. Recording locally
writes to the same gitignored `docs/public/demos`.

## Layout

| File             | Purpose                                                         |
| ---------------- | --------------------------------------------------------------- |
| `generate.sh`    | Builds `sal`, prepares each scenario, and runs every tape        |
| `scenario.sh`    | Rebuilds `~/sal-demo` into the state a tape starts from          |
| `common.tape`    | Terminal size, theme, and typing speed shared by every tape      |
| `prompt.tape`    | Hidden setup that gives every recording the same bare `$` prompt |
| `fixtures/`      | The RDF sources copied into the demo project                     |
| `<command>.tape` | One recording per documented command                             |

## Recording locally

Needs `vhs`, `ttyd`, `ffmpeg`, and `duckdb` on `PATH`:

```sh
make demos                       # every tape
./docs/demos/generate.sh build   # just docs/demos/build.tape
```

`generate.sh` builds `sal` from the working tree into `docs/demos/.bin`, so a tape
always records the code you have checked out rather than an installed release.

## Adding a demo

1. Write `docs/demos/<name>.tape`. Start it with
   `Output docs/public/demos/sal-<name>.gif`, then `Source docs/demos/common.tape` and
   `Source docs/demos/prompt.tape`. Paths in a tape are relative to the repository
   root, since that is where `generate.sh` runs `vhs`.
2. Add `<name>` to `TAPES` and `state_for` in `generate.sh`, choosing the starting
   state the command needs. Add a new state to `scenario.sh` if none of the existing
   ones fit.
3. Embed the GIF in `commands.mdx` at `/sal/demos/sal-<name>.gif`.

Commands that need credentials or that block — `push`, `clone`, `pull`, `upload`,
`serve` — are deliberately not recorded.

## Notes

- `scenario.sh` deletes and recreates `~/sal-demo` before every tape, so no recording
  depends on what an earlier one left behind. It writes only to that directory and to
  the user-level `~/.sal` that `sal init` creates.
- The demo project's Git remote is `https://github.com/cgs-earth/sal-demo.git`. SAL
  turns that remote into the base IRI for relative subjects, so it is visible in
  `sal query` output and is kept short on purpose.
- `vhs validate "docs/demos/*.tape"` checks tape syntax without recording anything.
