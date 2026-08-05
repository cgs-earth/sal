# Selects which of the two runtime stages below becomes the final image. `false`
# ships the bare CLI; `true` adds the sample data and the entrypoint that builds
# and serves it, useful for the cloudbuild demo deployment.
ARG DEMO=false

FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS go-builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH

# -trimpath removes local filesystem paths from the binary.
# -ldflags="-s -w" strips symbol and debug tables to keep the image smaller.
RUN CGO_ENABLED=0 \
    GOOS=$TARGETOS \
    GOARCH=$TARGETARCH \
    go build \
    -trimpath \
    -ldflags="-s -w" \
    -o sal .


# `sal query` shells out to the duckdb CLI, so ship the standalone binary.
FROM --platform=$BUILDPLATFORM debian:bookworm-slim AS duckdb-fetcher

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    unzip \
    && rm -rf /var/lib/apt/lists/*

ARG DUCKDB_VERSION=1.5.5
ARG TARGETARCH

RUN curl -fsSL -o /tmp/duckdb.zip \
    "https://github.com/duckdb/duckdb/releases/download/v${DUCKDB_VERSION}/duckdb_cli-linux-${TARGETARCH}.zip" \
    && unzip -o /tmp/duckdb.zip -d /usr/local/bin \
    && chmod +x /usr/local/bin/duckdb \
    && rm /tmp/duckdb.zip


FROM debian:bookworm-slim AS runtime

# git is a runtime dependency: sal shells out to it for project metadata,
# `sal clone`, and cloning sal modules.
RUN apt-get update && apt-get install -y \
    ca-certificates \
    git \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=go-builder /app/sal /app/sal
COPY --from=duckdb-fetcher /usr/local/bin/duckdb /usr/local/bin/duckdb

# Bake in the extensions every sal query loads, so a container does not need to
# reach extensions.duckdb.org before it can answer its first query. avro and
# httpfs are not installed by sal itself; iceberg loads the first and needs the
# second to scan a table on object storage.
RUN duckdb -c "INSTALL iceberg; INSTALL avro; INSTALL httpfs; INSTALL spatial;" \
    && duckdb -c "LOAD iceberg; LOAD spatial;"


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
