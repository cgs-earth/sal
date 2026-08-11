package validate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	rdflibgo "github.com/tggo/goRDFlib"
)

const (
	owlNamespaceIRI     = "http://www.w3.org/2002/07/owl#"
	dctermsNamespaceIRI = "http://purl.org/dc/terms/"
	// sha256VersionScheme heads the owl:versionIRI of a pinned vocabulary, so that
	// the version a project resolves a prefix against names the exact bytes behind it
	sha256VersionScheme = "urn:sha256:"
	// gitCommitVersionScheme heads the owl:versionIRI of a pinned salmodule://
	// vocabulary instead: the git commit hash of the module repository the
	// ontology was read from, so that a build the module's own code changed can
	// be told apart even when the ontology document it prints did not
	gitCommitVersionScheme = "urn:git-commit-hash:"
)

// PinnedVersion overrides the version a document is pinned at instead of the
// SHA-256 of its bytes. The zero value means no override: a pin gets
// sha256VersionScheme and the digest of the document. Fetch returns this for a
// salmodule:// vocabulary, pinning it at gitCommitVersionScheme instead.
type PinnedVersion struct {
	Scheme string
	Value  string
}

// PinnedVocabularies is the set of vocabulary documents a project resolves its
// prefixes against. It is read from and written to .sal/ns-prefix-versions.jsonld,
// which records a prefix's namespace against the SHA-256 of the document behind
// it -- or, for a salmodule:// vocabulary, the git commit hash of the module
// repository it was built from; the document itself is stored under .sal/data
// named by that same version, so the data product carries the vocabularies it
// was validated against and a later build validates against the same versions
// the first one did.
//
// A store with no path is ephemeral: nothing is read from disk, Save writes
// nothing, and every document is fetched. That is what validating RDF outside a
// SAL project uses.
type PinnedVocabularies struct {
	path    string
	blobDir string

	// Refresh ignores the versions already pinned so that every document is
	// fetched again and pinned anew. It is what --no-cache does.
	Refresh bool
	// Fetch dereferences a vocabulary document. Tests replace it. The returned
	// PinnedVersion overrides how the document is pinned; a salmodule:// source
	// returns one, everything else returns the zero value.
	Fetch func(string) ([]byte, string, PinnedVersion, error)

	entries map[string]pinnedVocabulary
	// fetched memoizes by source URL so two namespaces that resolve to the same
	// document are only dereferenced once
	fetched map[string]fetchedDocument
	// pending holds documents fetched this run, keyed by the version they are
	// pinned at, until Save writes them out
	pending map[string][]byte
	dirty   bool
}

type pinnedVocabulary struct {
	// Scheme and Version together are this pin's owl:versionIRI: Scheme is
	// sha256VersionScheme or gitCommitVersionScheme, and Version is the digest or
	// commit hash it names. Version also names the file the document is stored
	// under in blobDir.
	Scheme    string
	Version   string
	MediaType string
	Modified  time.Time
}

type fetchedDocument struct {
	body      []byte
	mediaType string
	version   PinnedVersion
}

// EphemeralVocabularies returns a store that pins nothing. Every document it is
// asked for is fetched, and Save does nothing.
func EphemeralVocabularies() *PinnedVocabularies {
	return &PinnedVocabularies{
		Fetch:   fetchVocabularyDocument,
		entries: map[string]pinnedVocabulary{},
		fetched: map[string]fetchedDocument{},
		pending: map[string][]byte{},
	}
}

// LoadPinnedVocabularies reads the versions a project has pinned. A project that
// has never pinned anything is not an error; it starts with an empty set.
func LoadPinnedVocabularies(path string, blobDir string) (*PinnedVocabularies, error) {
	pins := EphemeralVocabularies()
	pins.path = path
	pins.blobDir = blobDir

	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return pins, nil
	}
	if err != nil {
		return nil, err
	}
	if err := pins.unmarshal(content); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return pins, nil
}

// Document returns the vocabulary document recorded for id, fetching and
// pinning it when the project has no usable pin for it yet. id is what the pin
// is recorded under, either a prefix's namespace or an imported ontology's IRI,
// and source is the URL that document is dereferenced from, which for a
// namespace written with a fragment is not the namespace itself. The returned
// bool reports whether the document came from the project's pins rather than
// from its source.
func (p *PinnedVocabularies) Document(id string, source string) ([]byte, string, bool, error) {
	if !p.Refresh {
		if entry, ok := p.entries[id]; ok {
			body, err := p.readDocument(entry)
			if err == nil {
				return body, entry.MediaType, true, nil
			}
			// a pin whose document is missing is fetched again rather than
			// failing the run: .sal/data is not in git, so a fresh clone has the
			// pins before it has the documents they name
			slog.Warn("Pinned vocabulary " + id + " is not available locally, so it is being fetched again: " + err.Error())
		}
	}

	if doc, ok := p.fetched[source]; ok {
		p.Pin(id, doc.body, doc.mediaType, doc.version)
		return doc.body, doc.mediaType, false, nil
	}

	body, mediaType, version, err := p.Fetch(source)
	if err != nil {
		return nil, "", false, err
	}
	p.fetched[source] = fetchedDocument{body: body, mediaType: mediaType, version: version}
	p.Pin(id, body, mediaType, version)
	return body, mediaType, false, nil
}

// Pin records a document as the version of id the project resolves against.
// version overrides what the document is pinned at instead of the SHA-256 of
// body; pass the zero value for a vocabulary with no such override.
func (p *PinnedVocabularies) Pin(id string, body []byte, mediaType string, version PinnedVersion) {
	if version.Scheme == "" {
		version = PinnedVersion{Scheme: sha256VersionScheme, Value: documentDigest(body)}
	}
	if existing, ok := p.entries[id]; ok && existing.Scheme == version.Scheme && existing.Version == version.Value && existing.MediaType == mediaType {
		return
	}
	p.entries[id] = pinnedVocabulary{Scheme: version.Scheme, Version: version.Value, MediaType: mediaType, Modified: time.Now().UTC()}
	p.pending[version.Value] = body
	p.dirty = true
}

// Unpin drops the pin for id, so that a document SAL turned out not to be able
// to read is not recorded as the version the project resolves against.
func (p *PinnedVocabularies) Unpin(id string) {
	if _, ok := p.entries[id]; !ok {
		return
	}
	delete(p.entries, id)
	p.dirty = true
}

// Documents returns the path of every vocabulary document the pins name.
func (p *PinnedVocabularies) Documents() []string {
	paths := make([]string, 0, len(p.entries))
	seen := map[string]bool{}
	for _, entry := range p.entries {
		path := p.documentPath(entry.Version)
		if seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// AppendProvenance adds an owl:Ontology node for every vocabulary the project
// pins to graph, carrying the same owl:versionIRI, dcterms:format, and
// dcterms:modified a build writes to .sal/ns-prefix-versions.jsonld. This is
// what makes a pinned vocabulary's exact version queryable alongside the data
// it validated, rather than only recorded in the lockfile on disk.
func (p *PinnedVocabularies) AppendProvenance(graph *rdflibgo.Graph) {
	owlOntology := rdflibgo.NewURIRefUnsafe(owlNamespaceIRI + "Ontology")
	versionIRI := rdflibgo.NewURIRefUnsafe(owlNamespaceIRI + "versionIRI")
	format := rdflibgo.NewURIRefUnsafe(dctermsNamespaceIRI + "format")
	modified := rdflibgo.NewURIRefUnsafe(dctermsNamespaceIRI + "modified")

	ids := make([]string, 0, len(p.entries))
	for id := range p.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		entry := p.entries[id]
		subject := rdflibgo.NewURIRefUnsafe(id)
		graph.Add(subject, rdflibgo.RDF.Type, owlOntology)
		graph.Add(subject, versionIRI, rdflibgo.NewURIRefUnsafe(entry.Scheme+entry.Version))
		if entry.MediaType != "" {
			graph.Add(subject, format, rdflibgo.NewLiteral(entry.MediaType))
		}
		if !entry.Modified.IsZero() {
			graph.Add(subject, modified, rdflibgo.NewLiteral(entry.Modified.Format(time.RFC3339), rdflibgo.WithDatatype(rdflibgo.XSDDateTime)))
		}
	}
}

// Save writes the pins and the documents they name. It does nothing when
// nothing changed, so a build that resolved every prefix from its pins leaves
// the project's git worktree clean.
func (p *PinnedVocabularies) Save() error {
	if p.path == "" || !p.dirty {
		return nil
	}

	if err := p.writeDocuments(); err != nil {
		return err
	}
	content, err := p.marshal()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(p.path, content, 0644); err != nil {
		return err
	}

	slog.Info(fmt.Sprintf("Pinned %d vocabulary versions in %s", len(p.entries), p.path))
	p.dirty = false
	return nil
}

func (p *PinnedVocabularies) writeDocuments() error {
	if err := os.MkdirAll(p.blobDir, 0755); err != nil {
		return err
	}
	for _, entry := range p.entries {
		body, ok := p.pending[entry.Version]
		if !ok {
			continue
		}
		path := p.documentPath(entry.Version)
		// the name of a document is the version it is pinned at, so one already
		// on disk is the one being written
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.WriteFile(path, body, 0644); err != nil {
			return err
		}
	}
	return nil
}

// readDocument returns a pinned document. A document pinned by the digest of
// its contents is checked to still hash to that version; a document pinned by
// a git commit hash cannot be verified this way, since the hash does not
// derive from the document's bytes, so its presence on disk is trusted.
func (p *PinnedVocabularies) readDocument(entry pinnedVocabulary) ([]byte, error) {
	if body, ok := p.pending[entry.Version]; ok {
		return body, nil
	}
	if p.blobDir == "" {
		return nil, fmt.Errorf("no vocabulary directory to read %s from", entry.Version)
	}

	path := p.documentPath(entry.Version)
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if entry.Scheme == sha256VersionScheme {
		if actual := documentDigest(body); actual != entry.Version {
			return nil, fmt.Errorf("%s hashes to %s rather than the pinned %s", path, actual, entry.Version)
		}
	}
	return body, nil
}

func (p *PinnedVocabularies) documentPath(version string) string {
	return filepath.Join(p.blobDir, version)
}

func documentDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// pinnedVocabulariesDocument is the JSON-LD shape of
// .sal/ns-prefix-versions.jsonld. The file is generated on every build that
// pins something new, so a hand edit to it does not survive.
type pinnedVocabulariesDocument struct {
	Context map[string]string      `json:"@context"`
	Graph   []pinnedVocabularyNode `json:"@graph"`
}

type pinnedVocabularyNode struct {
	ID         string        `json:"@id"`
	Type       string        `json:"@type"`
	VersionIRI iriValue      `json:"owl:versionIRI"`
	Format     string        `json:"dcterms:format,omitempty"`
	Modified   *typedLiteral `json:"dcterms:modified,omitempty"`
}

type iriValue struct {
	ID string `json:"@id"`
}

type typedLiteral struct {
	Value string `json:"@value"`
	Type  string `json:"@type"`
}

var pinnedVocabulariesContext = map[string]string{
	"owl":     owlNamespaceIRI,
	"dcterms": dctermsNamespaceIRI,
	"xsd":     xsdNamespaceIRI,
}

func (p *PinnedVocabularies) marshal() ([]byte, error) {
	ids := make([]string, 0, len(p.entries))
	for id := range p.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	doc := pinnedVocabulariesDocument{Context: pinnedVocabulariesContext, Graph: make([]pinnedVocabularyNode, 0, len(ids))}
	for _, id := range ids {
		entry := p.entries[id]
		node := pinnedVocabularyNode{
			ID:         id,
			Type:       "owl:Ontology",
			VersionIRI: iriValue{ID: entry.Scheme + entry.Version},
			Format:     entry.MediaType,
		}
		if !entry.Modified.IsZero() {
			node.Modified = &typedLiteral{Value: entry.Modified.Format(time.RFC3339), Type: "xsd:dateTime"}
		}
		doc.Graph = append(doc.Graph, node)
	}

	content, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}

func (p *PinnedVocabularies) unmarshal(content []byte) error {
	var doc pinnedVocabulariesDocument
	if err := json.Unmarshal(content, &doc); err != nil {
		return err
	}

	for _, node := range doc.Graph {
		if node.ID == "" {
			return fmt.Errorf("a pinned vocabulary has no @id")
		}
		scheme, version, ok := splitVersionIRI(node.VersionIRI.ID)
		if !ok {
			return fmt.Errorf("%s is pinned at %q rather than a %s or %s version", node.ID, node.VersionIRI.ID, sha256VersionScheme, gitCommitVersionScheme)
		}
		entry := pinnedVocabulary{Scheme: scheme, Version: version, MediaType: node.Format}
		if node.Modified != nil {
			// a modification time SAL cannot read is not worth failing a build
			// over; it is recorded for a human rather than resolved against
			entry.Modified, _ = time.Parse(time.RFC3339, node.Modified.Value)
		}
		p.entries[node.ID] = entry
	}
	return nil
}

// splitVersionIRI splits a pinned vocabulary's owl:versionIRI into the scheme
// it is pinned under and the digest or commit hash it names.
func splitVersionIRI(versionIRI string) (scheme string, version string, ok bool) {
	if digest, found := strings.CutPrefix(versionIRI, sha256VersionScheme); found {
		if len(digest) != sha256.Size*2 {
			return "", "", false
		}
		return sha256VersionScheme, digest, true
	}
	if commit, found := strings.CutPrefix(versionIRI, gitCommitVersionScheme); found {
		if commit == "" {
			return "", "", false
		}
		return gitCommitVersionScheme, commit, true
	}
	return "", "", false
}
