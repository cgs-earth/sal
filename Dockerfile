# Selects which of the runtime stages below becomes the final image. `false`
# ships the bare CLI; `true` adds the sample data and the entrypoint that builds
# and serves it; `baked` is `true` with the demo project already built and
# copied in, which is what the cloudbuild demo deployment ships, since the
# sample data includes a SAL module task and Cloud Run has no docker daemon to
# run it with.
ARG DEMO=false

FROM golang:1.25-bookworm AS imports

WORKDIR /app
COPY . .

# Writes the list of packages the project imports from its dependencies, one per
# line, so the go-builder stage below can compile them in a layer of their own.
# `go list -e` reads the imports out of the project's own source without needing
# the dependencies to be present, and GOPROXY=off keeps it from fetching them.
# The first path element of a standard library package never contains a dot, so
# the grep keeps only third party imports, minus the project's own packages.
RUN GOPROXY=off go list -e -f '{{join .Imports "\n"}}' ./... 2>/dev/null \
    | grep '^[^/]*\.' \
    | grep -v "^$(go list -m)/" \
    | sort -u > /imports.txt


# DuckDB is a C++ library linked into the sal binary, so this build needs cgo and
# a toolchain for the architecture it is producing. Building natively on the
# target platform is what keeps that simple: BuildKit runs this stage under
# emulation for a cross build rather than needing a cross compiler installed. The
# push_to_ghcr workflow only builds linux/amd64, so nothing is emulated in CI.
FROM golang:1.25-bookworm AS go-builder

WORKDIR /app

# Nearly all of the compile time is the dependencies, and cgo compiling the
# DuckDB binding is the bulk of that, so they are compiled first in a layer keyed
# only on go.mod, go.sum, and the import list: `go build` of those packages
# downloads their modules and leaves everything compiled in the Go build cache,
# which the build of the project below then reuses. The layer is reused across
# builds until a dependency changes, so a commit that only touches sal's own
# code compiles sal's own packages and links, a few seconds' work. The build
# flags must match the final build's, since they are part of the cache key.
COPY go.mod go.sum ./
COPY --from=imports /imports.txt /imports.txt
RUN CGO_ENABLED=1 go build -trimpath $(cat /imports.txt)

COPY . .

# -trimpath removes local filesystem paths from the binary.
# -ldflags="-s -w" strips symbol and debug tables to keep the image smaller.
#
# The DuckDB library itself is linked statically, so no libduckdb has to be
# shipped and no duckdb CLI has to be installed. The binary is not fully static:
# DuckDB dlopens its extensions, which a statically linked glibc cannot do, so
# libstdc++ and glibc stay dynamic and the runtime stage installs them.
RUN CGO_ENABLED=1 \
    go build \
    -trimpath \
    -ldflags="-s -w" \
    -o sal .


FROM debian:bookworm-slim AS runtime

# git is a runtime dependency: sal shells out to it for project metadata,
# `sal clone`, and cloning sal modules. libstdc++6 is what the linked in DuckDB
# needs, and it is not in the slim image by default.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    git \
    libstdc++6 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=go-builder /app/sal /app/sal

# Bake in the extensions every sal query loads, so a container does not need to
# reach extensions.duckdb.org before it can answer its first query. Only the
# extensions DuckDB is compiled with are linked in; iceberg reads the table,
# httpfs lets it do so on object storage, avro reads Iceberg manifests, and
# spatial handles geometry objects.
RUN /app/sal duckdb-extensions


# DEMO=false: the plain CLI, with no sample data and no startup work.
FROM runtime AS runtime-false

ENTRYPOINT [ "/app/sal" ]


# DEMO=true: the entrypoint builds the RDF copied in here into a sample table on
# startup, so a `sal serve` deployment has something to explore on a first visit.
# portolan_export.ttl declares a SAL module task that pulls a few ArcGIS layers
# in as STAC catalogs; the entrypoint runs it when it can reach a docker daemon
# and leaves it out otherwise.
FROM runtime AS runtime-true

COPY build/testdata/correct/ /app/demo-data/
COPY build/testdata/reference/portolan_export.ttl /app/demo-data/
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

EXPOSE 8080

ENTRYPOINT [ "/app/entrypoint.sh" ]
CMD [ "serve", "--with-ui" ]


# DEMO=baked: the demo project the entrypoint would build on startup, built
# ahead of time where a docker daemon was available and handed in as the `demo`
# build context (`--build-context demo=<dir>`). It sits at the path the
# entrypoint builds into, so the entrypoint finds the built data and serves it
# rather than building again; the table's recorded paths are right because the
# project was built at this same path. Only evaluated when DEMO=baked, so a
# build without the context is unaffected.
FROM runtime-true AS runtime-baked

COPY --from=demo / /app/demo/


# ENTRYPOINT cannot be made conditional on its own, so the DEMO build arg picks
# the stage that becomes the final image instead.
FROM runtime-${DEMO}
