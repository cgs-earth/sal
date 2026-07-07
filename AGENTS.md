# SAL

SAL, (semantic accessibility layer), is a CLI tool for creating RDF data and metadata from a dataset. It contains a series of subcommands. Each subcommand is present in a subdirectory of the same name. 

## Subcommands

### `build`

- Validates input RDF data. This data can be defined in either Turtle or JSON-LD. 
- For all input RDF data, if there is a term that is not defined in the provided prefixes, SAL will throw an error which calls out the specific line number with the offending term.
    - For instance, if the user makes a typo and specifies `schema:nameee` in their JSON-LD, SAL build will throw an error saying that `nameee` is not a defined term in the RDF vocab. This should be supported for any generalized RDF vocabulary.

### `clone`

- Takes in a full URL to an OCI artifact. 
- It uses oras to introspect the manifest info about the artifact. It retrieves the value of the `org.opencontainers.image.source` to determine the location of the source code. It finds the pinned commit hash using the value of `sal.git-commit-hash`.
    - It then clones the source code into the current working directory.
    - It checks out the commit hash specified in the manifest.
    - Within the newly cloned repo directory, it should run `sal init` to initialize a sal project.
    - Within the new cloned repo directory, it should pull the OCI artifact and place it in the `.sal/data` directory.

## Code Style

- Use testify for writing succinct tests; avoid just the standard library and if statements with `t.Error()`.
- If a function would only be called from one other function and it is short, try to just condense it into the function that calls it.
- If some functionality would be very complex, duplicative, and better handled by an underlying library like json-gold or goRDFlib, say so and mark it as TODO in the code.
- Any function with functionality that is non trivial should be documented with a succinct comment of what it does.
- Don't create functions that are very small and only used in a single place.
- Do not use table oriented tests; make each test case a separate test function so it is easier to debug

## Development 

- `go test ./...` should pass after every new feature