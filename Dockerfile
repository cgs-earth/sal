# Selects which of the two runtime stages below becomes the final image. `false`
# ships the bare CLI; `true` adds the sample data and the entrypoint that builds
# and serves it, useful for the cloudbuild demo deployment.
ARG DEMO=false

# DuckDB is a C++ library linked into the sal binary, so this build needs cgo and
# a toolchain for the architecture it is producing. Building natively on the
# target platform is what keeps that simple: BuildKit runs this stage under
# emulation for a cross build rather than needing a cross compiler installed. The
# push_to_ghcr workflow only builds linux/amd64, so nothing is emulated in CI.
FROM golang:1.25-bookworm AS go-builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

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
FROM runtime AS runtime-true

COPY build/testdata/correct/ /app/demo-data/
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

EXPOSE 8080

ENTRYPOINT [ "/app/entrypoint.sh" ]
CMD [ "serve", "--with-ui" ]


# ENTRYPOINT cannot be made conditional on its own, so the DEMO build arg picks
# the stage that becomes the final image instead.
FROM runtime-${DEMO}
