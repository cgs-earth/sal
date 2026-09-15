package salmodule

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	rdflibgo "github.com/tggo/goRDFlib"
)

// ProvFile is the name of the JSON-LD document in the blob store that records
// every file and directory a SAL module task has ever copied there: its
// digest, the name the task gave it, the module that produced it, when it was
// written, and where it sits under the blob store. It is an append-only log.
// A run adds the copies it kept and never removes what an earlier run left,
// so the blob store accumulates every version a task has produced until the
// user clears it. The shape is the one .sal/config.jsonld records a pinned
// vocabulary in.
const ProvFile = "prov.jsonld"

// provContext is the @context of prov.jsonld, the prefixes its nodes are
// written with.
var provContext = map[string]string{
	"dcterms": "http://purl.org/dc/terms/",
	"owl":     "http://www.w3.org/2002/07/owl#",
	"rdfs":    "http://www.w3.org/2000/01/rdf-schema#",
	"xsd":     "http://www.w3.org/2001/XMLSchema#",
}

// provDocument is prov.jsonld as encoding/json reads and writes it.
type provDocument struct {
	Context map[string]string `json:"@context"`
	Graph   []provNode        `json:"@graph"`
}

// provNode is one copy in prov.jsonld. Its @id is the urn:sha256: IRI of the
// copy's contents, the same IRI the graph refers to the copy by, so that two
// modules naming a path the same way never share a record; its rdfs:label is
// the file:/// IRI the task named it with, ending in a slash for a directory;
// its dcterms:identifier is where the copy sits under the blob store; and its
// dcterms:source is the module whose task produced it.
type provNode struct {
	ID         string         `json:"@id"`
	Label      string         `json:"rdfs:label"`
	VersionIRI provReference  `json:"owl:versionIRI"`
	Identifier string         `json:"dcterms:identifier,omitempty"`
	Source     *provReference `json:"dcterms:source,omitempty"`
	Modified   provDateTime   `json:"dcterms:modified"`
	Comment    string         `json:"rdfs:comment"`
}

type provReference struct {
	ID string `json:"@id"`
}

type provDateTime struct {
	Value string `json:"@value"`
	Type  string `json:"@type"`
}

// Provenance is the contents of a blob store's prov.jsonld.
type Provenance struct {
	nodes []provNode
}

// LoadProvenance reads prov.jsonld from blobDir. A blob store without one
// holds no copies and loads empty.
func LoadProvenance(blobDir string) (*Provenance, error) {
	content, err := os.ReadFile(filepath.Join(blobDir, ProvFile))
	if errors.Is(err, os.ErrNotExist) {
		return &Provenance{}, nil
	}
	if err != nil {
		return nil, err
	}
	var document provDocument
	if err := json.Unmarshal(content, &document); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(blobDir, ProvFile), err)
	}
	return &Provenance{nodes: document.Graph}, nil
}

// blobPath is where the recorded copy sits relative to the blob store, ending
// in a slash for a directory: its dcterms:identifier, or the bare digest of
// its @id for a record without one.
func (n provNode) blobPath() string {
	if n.Identifier != "" {
		return n.Identifier
	}
	return strings.TrimPrefix(n.ID, digestIRIPrefix)
}

// BlobPaths returns the path, relative to the blob store and ending in a
// slash for a directory, of every copy prov.jsonld records.
func (p *Provenance) BlobPaths() []string {
	paths := make([]string, 0, len(p.nodes))
	for _, node := range p.nodes {
		paths = append(paths, node.blobPath())
	}
	return paths
}

// Label returns the file:/// IRI the task named the copy at blobPath with,
// and whether prov.jsonld records a copy there.
func (p *Provenance) Label(blobPath string) (string, bool) {
	for _, node := range p.nodes {
		if node.blobPath() == blobPath && node.Label != "" {
			return node.Label, true
		}
	}
	return "", false
}

// AppendProvenance describes every copy prov.jsonld records in graph, under
// the urn:sha256: IRI of its contents, the way LinkCopiedFiles describes a
// copy when it is made: its file:/// IRI as rdfs:label, when it was written as
// dcterms:modified, its path under the blob store as dcterms:identifier, the
// module that produced it as dcterms:source, and its digest as
// owl:versionIRI, plus the rdfs:comment the record carries. It is what carries
// a copy made by an earlier run into a table built again from source, so that
// the table describes every copy the blob store holds and a copy is listed
// with the pinned vocabularies. A statement the graph already holds is left as
// it is. It returns how many copies were described.
func (p *Provenance) AppendProvenance(graph *rdflibgo.Graph) int {
	described := 0
	for _, node := range p.nodes {
		if node.VersionIRI.ID == "" {
			continue
		}
		subject := rdflibgo.NewURIRefUnsafe(node.VersionIRI.ID)
		graph.Add(subject, rdflibgo.NewURIRefUnsafe(owlVersionIRI), subject)
		if node.Label != "" {
			graph.Add(subject, rdflibgo.NewURIRefUnsafe(rdfsLabelIRI), rdflibgo.NewLiteral(node.Label))
		}
		if path := node.blobPath(); path != "" {
			graph.Add(subject, rdflibgo.NewURIRefUnsafe(dctermsIdentifierIRI), rdflibgo.NewLiteral(path))
		}
		if node.Source != nil && node.Source.ID != "" {
			graph.Add(subject, rdflibgo.NewURIRefUnsafe(dctermsSourceIRI), rdflibgo.NewURIRefUnsafe(node.Source.ID))
		}
		if node.Modified.Value != "" {
			graph.Add(subject, rdflibgo.NewURIRefUnsafe(dctermsModifiedIRI), rdflibgo.NewLiteral(node.Modified.Value, rdflibgo.WithDatatype(rdflibgo.XSDDateTime)))
		}
		if node.Comment != "" {
			graph.Add(subject, rdflibgo.NewURIRefUnsafe(rdfsCommentIRI), rdflibgo.NewLiteral(node.Comment))
		}
		described++
	}
	return described
}

// recordCopies appends each copy to prov.jsonld under blobDir. A copy whose
// digest is already recorded is the same contents stored again, so its record
// is replaced rather than repeated; nothing else in the file is touched, since
// the file is a log of everything the blob store has been handed.
func recordCopies(blobDir string, copies []CopiedFile) error {
	provenance, err := LoadProvenance(blobDir)
	if err != nil {
		return err
	}
	for _, copied := range copies {
		kind := "file"
		served := "the file is served under that digest"
		if copied.Directory {
			kind = "directory"
			served = "the directory is served whole as a zip archive under that digest"
		}
		node := provNode{
			ID:         copied.IRI(),
			Label:      copied.FileIRI(),
			VersionIRI: provReference{ID: copied.IRI()},
			Identifier: copied.BlobPath,
			Modified:   provDateTime{Value: copied.Modified.Format(time.RFC3339), Type: "xsd:dateTime"},
			Comment: fmt.Sprintf("Represents the %s %s a task of the SAL module %s copied out of its container as of %s. %s is the SHA-256 of its contents, and %s.",
				kind, copied.FileIRI(), copied.Source, copied.Modified.Format(time.RFC3339), copied.IRI(), served),
		}
		if copied.Source != "" {
			node.Source = &provReference{ID: copied.Source}
		}
		replaced := false
		for i := range provenance.nodes {
			if provenance.nodes[i].ID == node.ID {
				provenance.nodes[i] = node
				replaced = true
				break
			}
		}
		if !replaced {
			provenance.nodes = append(provenance.nodes, node)
		}
	}
	return writeProvenance(blobDir, provenance.nodes)
}

// RemoveCopiedFiles deletes every file and directory prov.jsonld under blobDir
// records, and prov.jsonld itself. It is what a wipe does with them: they are
// artifacts of a run, regenerated by the next one, and without the table that
// refers to them nothing resolves their digests. It is the only time sal
// removes a copy; a run only ever adds to the blob store.
func RemoveCopiedFiles(blobDir string) ([]string, error) {
	provenance, err := LoadProvenance(blobDir)
	if err != nil {
		return nil, err
	}
	paths := provenance.BlobPaths()
	for _, path := range paths {
		relative := filepath.FromSlash(strings.TrimSuffix(path, "/"))
		// a path that would reach outside the blob store, which only a
		// hand-edited prov.jsonld could carry, is refused rather than followed
		if relative == "" || !filepath.IsLocal(relative) {
			return nil, fmt.Errorf("%s records %q, which is not inside the blob store", ProvFile, path)
		}
		if err := os.RemoveAll(filepath.Join(blobDir, relative)); err != nil {
			return nil, fmt.Errorf("remove the copy of %s: %w", path, err)
		}
	}
	if err := os.Remove(filepath.Join(blobDir, ProvFile)); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return paths, nil
}

// writeProvenance writes nodes as prov.jsonld under blobDir, sorted by @id so
// the file is stable across runs.
func writeProvenance(blobDir string, nodes []provNode) error {
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	content, err := json.MarshalIndent(provDocument{Context: provContext, Graph: nodes}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(blobDir, ProvFile), append(content, '\n'), 0644)
}
