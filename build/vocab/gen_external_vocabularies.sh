#!/bin/sh
set -e

cd "$(dirname "$0")"

https_uri="https://schema.org/"
http_uri="http://schema.org/"

https_file="$(printf '%s' "$https_uri" | jq -sRr @uri).jsonld"
http_file="$(printf '%s' "$http_uri" | jq -sRr @uri).jsonld"

curl -fsSL https://schema.org/version/latest/schemaorg-current-https.jsonld \
    -o "$https_file"

curl -fsSL https://schema.org/version/latest/schemaorg-current-http.jsonld \
    -o "$http_file"

echo "Finished downloading vocabularies."