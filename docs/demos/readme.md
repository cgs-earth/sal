# Demo recordings

The terminal GIFs embedded in [the command reference](../src/content/docs/reference/commands.mdx),
recorded with [VHS](https://github.com/charmbracelet/vhs).

The GIFs are **not committed**. `.github/workflows/demos.yml` records them from the
working tree and uploads them as `demo-gifs-<tape>` artifacts; `push_pages.yml` merges
those into `docs/public/demos` before building the site, so what the docs show is
always what the current CLI prints. Recording locally writes to the same gitignored
`docs/public/demos`.

A full recording only happens where something reads the result, which is a deploy of
main. `push_pages.yml` calls this workflow with `record: true`; it builds `sal` once in
a `build-sal` job and then records the tapes as a matrix, one job per tape, with
`SAL_DEMO_SKIP_BUILD=1` so the binary is compiled once rather than five times.

A **branch push** runs the `check` job alone: it downloads the `vhs` binary and parses
the tapes, the recording scripts, and the wiring between them. No `sal` build, no
recording toolchain, no GIFs, no artifacts — nothing would read them before the branch
merges. Use `workflow_dispatch` on the branch to force a full recording.

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

Needs `vhs`, `ttyd`, and `ffmpeg` on `PATH`:

```sh
make demos                       # every tape
./docs/demos/generate.sh build   # just docs/demos/build.tape
```

`generate.sh` builds `sal` from the working tree into `docs/demos/.bin`, so a tape
always records the code you have checked out rather than an installed release. Set
`SAL_DEMO_SKIP_BUILD=1` to reuse whatever is already at `docs/demos/.bin/sal`.

## Checking what a tape recorded

VHS renders text as well as video, which is the quickest way to see what a tape
actually put on screen — and the only way if ffmpeg is unavailable, since the text is
written before the encode:

```sh
sed 's|^Output .*|Output /tmp/out.txt|' docs/demos/build.tape > /tmp/build.tape
./docs/demos/scenario.sh committed
vhs /tmp/build.tape && tail -30 /tmp/out.txt
```

Frames are separated by a rule of 80 dashes, so the last one is the frame the GIF
rests on. This catches wrapped tables and truncated output that are otherwise
invisible until the GIF is published. It under-captures the SQL shell's redraws,
though, so `query.tape` still has to be checked as a real GIF.

## Adding a demo

1. Write `docs/demos/<name>.tape`. Start it with
   `Output docs/public/demos/sal-<name>.gif`, then `Source docs/demos/common.tape` and
   `Source docs/demos/prompt.tape`. Paths in a tape are relative to the repository
   root, since that is where `generate.sh` runs `vhs`.
2. Add `<name>` to `TAPES` and `state_for` in `generate.sh`, choosing the starting
   state the command needs. Add a new state to `scenario.sh` if none of the existing
   ones fit.
3. Add `<name>` to the `record` matrix in `.github/workflows/demos.yml`, or CI will
   never record it. The `check` job fails on a tape missing from either list.
4. Embed the GIF in `commands.mdx` at `/sal/demos/sal-<name>.gif`.

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
- Tapes wait for the prompt with `Wait` rather than sleeping for a guessed duration,
  so a slow runner cannot finish a command after its `Sleep` elapsed and record the
  output half drawn. `common.tape` sets the pattern that matches the demo prompt.
  `query.tape` sleeps instead: `sal query` opens a full screen shell, so the prompt
  the pattern matches is not on screen until `Ctrl+D` closes it again.
- `generate.sh` runs `sal duckdb-extensions` before `query.tape`. DuckDB is linked
  into `sal`, but its extensions are downloaded on first use, and left to itself that
  download happens inside the recording.
