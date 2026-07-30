// Package salmoduleintegration holds the SAL module integration tests. The
// tests themselves are behind the `integration` build tag because they build
// and run docker containers, which is far too slow for local test driven
// development. Run them with `make integration_test_salmodule`.
package salmoduleintegration
