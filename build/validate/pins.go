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
)

const (
	owlNamespaceIRI     = "http://www.w3.org/2002/07/owl#"
	dctermsNamespaceIRI = "http://purl.org/dc/terms/"
	// digestScheme heads the owl:versionIRI of a pinned vocabulary, so that the
	// version a project resolves a prefix against names the exact bytes behind it
	digestScheme = "urn:sha256:"
)

// PinnedVocabularies is the set of vocabulary documents a project resolves its
// prefixes against. It is read from and written to .sal/ns-prefix-versions.jsonld,
// which records a prefix's namespace against the SHA-256 of the document behind
// it; the document itself is stored under .sal/data named by that hash, so the
// data product carries the vocabularies it was validated against and a later
// build validates against the same versions the first one did.
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
	// Fetch dereferences a vocabulary document. Tests replace it.
	Fetch func(string) ([]byte, string, error)

	entries map[string]pinnedVocabulary
	// fetched memoizes by source URL so two namespaces that resolve to the same
	// document are only dereferenced once
	fetched map[string]fetchedDocument
	// pending holds documents fetched this run, keyed by digest, until Save
	// writes them out
	pending map[string][]byte
	dirty   bool
}

type pinnedVocabulary struct {
	Digest    string
	MediaType string
	Modified  time.Time
}

type fetchedDocument struct {
	body      []byte
	mediaType string
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
			body, err := p.readDocument(entry.Digest)
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
		p.Pin(id, doc.body, doc.mediaType)
		return doc.body, doc.mediaType, false, nil
	}

	body, mediaType, err := p.Fetch(source)
	if err != nil {
		return nil, "", false, err
	}
	p.fetched[source] = fetchedDocument{body: body, mediaType: mediaType}
	p.Pin(id, body, mediaType)
	return body, mediaType, false, nil
}

// Pin records a document as the version of id the project resolves against.
func (p *PinnedVocabularies) Pin(id string, body []byte, mediaType string) {
	digest := documentDigest(body)
	if existing, ok := p.entries[id]; ok && existing.Digest == digest && existing.MediaType == mediaType {
		return
	}
	p.entries[id] = pinnedVocabulary{Digest: digest, MediaType: mediaType, Modified: time.Now().UTC()}
	p.pending[digest] = body
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
		path := p.documentPath(entry.Digest)
		if seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
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
		body, ok := p.pending[entry.Digest]
		if !ok {
			continue
		}
		path := p.documentPath(entry.Digest)
		// the name of a document is the hash of its contents, so one already on
		// disk is the one being written
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.WriteFile(path, body, 0644); err != nil {
			return err
		}
	}
	return nil
}

// readDocument returns a pinned document, checking that it still hashes to the
// version it is pinned at.
func (p *PinnedVocabularies) readDocument(digest string) ([]byte, error) {
	if body, ok := p.pending[digest]; ok {
		return body, nil
	}
	if p.blobDir == "" {
		return nil, fmt.Errorf("no vocabulary directory to read %s from", digest)
	}

	path := p.documentPath(digest)
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if actual := documentDigest(body); actual != digest {
		return nil, fmt.Errorf("%s hashes to %s rather than the pinned %s", path, actual, digest)
	}
	return body, nil
}

func (p *PinnedVocabularies) documentPath(digest string) string {
	return filepath.Join(p.blobDir, digest)
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
			VersionIRI: iriValue{ID: digestScheme + entry.Digest},
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
		digest, ok := strings.CutPrefix(node.VersionIRI.ID, digestScheme)
		if !ok || len(digest) != sha256.Size*2 {
			return fmt.Errorf("%s is pinned at %q rather than a %s version", node.ID, node.VersionIRI.ID, digestScheme)
		}
		entry := pinnedVocabulary{Digest: digest, MediaType: node.Format}
		if node.Modified != nil {
			// a modification time SAL cannot read is not worth failing a build
			// over; it is recorded for a human rather than resolved against
			entry.Modified, _ = time.Parse(time.RFC3339, node.Modified.Value)
		}
		p.entries[node.ID] = entry
	}
	return nil
}
