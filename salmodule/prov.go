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
// every directory a SAL module task copied there: the path it sits at, its
// name, when it was last written, and the SHA-256 of its contents as
// owl:versionIRI. A single file is named by its digest, so it needs no
// record; a directory keeps the path the module gave it, so that a Zarr or
// STAC client can read it in place, and this file is what resolves its digest
// back to that path. The shape is the one .sal/config.jsonld records a pinned
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

// provNode is one copied directory in prov.jsonld. Its @id and rdfs:label are
// both the file:/// IRI the task named the directory with, ending in a slash,
// so that the record is known by the same name the task's output used. The
// directory's path under the blob store is that IRI's path without its leading
// slash, which blobPath derives.
type provNode struct {
	ID         string        `json:"@id"`
	Label      string        `json:"rdfs:label"`
	VersionIRI provReference `json:"owl:versionIRI"`
	Modified   provDateTime  `json:"dcterms:modified"`
	Comment    string        `json:"rdfs:comment"`
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
// holds no copied directories and loads empty.
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

// blobPath is where the recorded directory sits relative to the blob store,
// ending in a slash: the path of its file:/// IRI without the leading slash. A
// record written before the @id carried the scheme is already that path.
func (n provNode) blobPath() string { return strings.TrimPrefix(n.ID, fileIRIPrefix) }

// DirectoryPaths returns the path, relative to the blob store and ending in a
// slash, of every directory prov.jsonld records.
func (p *Provenance) DirectoryPaths() []string {
	paths := make([]string, 0, len(p.nodes))
	for _, node := range p.nodes {
		paths = append(paths, node.blobPath())
	}
	return paths
}

// DirectoryPath returns the path, relative to the blob store and ending in a
// slash, of the directory whose contents hash to digest, given bare or as
// urn:sha256:<digest>, and whether there is one.
func (p *Provenance) DirectoryPath(digest string) (string, bool) {
	versionIRI := "urn:sha256:" + strings.TrimPrefix(digest, "urn:sha256:")
	for _, node := range p.nodes {
		if node.VersionIRI.ID == versionIRI {
			return node.blobPath(), true
		}
	}
	return "", false
}

// AppendProvenance describes every directory prov.jsonld records in graph,
// under the urn:sha256: IRI of its contents, the way LinkCopiedFiles describes
// a copy when it is made: its file:/// IRI as rdfs:label, when it was written
// as dcterms:modified, its path under the blob store as dcterms:identifier, and
// its digest as owl:versionIRI, plus the rdfs:comment the record carries. It is
// what carries a directory copied by an earlier run into a table built again
// from source, so that the table describes every copy the blob store holds and
// a copy is listed with the pinned vocabularies. A statement the graph already
// holds is left as it is. It returns how many directories were described.
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
		if node.ID != "" {
			graph.Add(subject, rdflibgo.NewURIRefUnsafe(dctermsIdentifierIRI), rdflibgo.NewLiteral(node.blobPath()))
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

// recordDirectories adds each copied directory to prov.jsonld under blobDir,
// replacing the record of an earlier copy at the same path, since the path
// now holds the new copy.
func recordDirectories(blobDir string, directories []CopiedFile) error {
	provenance, err := LoadProvenance(blobDir)
	if err != nil {
		return err
	}
	for _, directory := range directories {
		node := provNode{
			ID:         directory.FileIRI(),
			Label:      directory.FileIRI(),
			VersionIRI: provReference{ID: directory.IRI()},
			Modified:   provDateTime{Value: directory.Modified.Format(time.RFC3339), Type: "xsd:dateTime"},
			Comment: fmt.Sprintf("Represents the directory %s a SAL module task copied out of its container as of %s. %s is the SHA-256 of its contents, and the directory is served whole as a zip archive under that digest.",
				directory.BlobPath, directory.Modified.Format(time.RFC3339), directory.IRI()),
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

// PruneProvenance makes prov.jsonld under blobDir record exactly the
// directories kept, the ones the run that just finished copied and linked:
// the record and the on-disk copy of any other directory, left by an earlier
// run whose task no longer produces it, are removed, and the file itself is
// removed when nothing is kept. It returns the blob-relative paths removed. A
// task that ran again replaced its directory in place, so what remains is
// what is on disk from the most recent run and nothing else.
func PruneProvenance(blobDir string, kept []CopiedFile) ([]string, error) {
	provenance, err := LoadProvenance(blobDir)
	if err != nil {
		return nil, err
	}
	keptPaths := map[string]bool{}
	for _, directory := range kept {
		if directory.Directory {
			keptPaths[directory.BlobPath] = true
		}
	}

	var removed []string
	nodes := provenance.nodes[:0]
	for _, node := range provenance.nodes {
		path := node.blobPath()
		if keptPaths[path] {
			nodes = append(nodes, node)
			continue
		}
		if err := removeCopiedDirectory(blobDir, path); err != nil {
			return nil, err
		}
		removed = append(removed, path)
	}
	if len(removed) == 0 {
		return nil, nil
	}
	if len(nodes) == 0 {
		if err := os.Remove(filepath.Join(blobDir, ProvFile)); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		return removed, nil
	}
	return removed, writeProvenance(blobDir, nodes)
}

// RemoveCopiedDirectories deletes every directory prov.jsonld under blobDir
// records, and prov.jsonld itself. It is what a wipe does with them: they are
// artifacts of a run, regenerated by the next one, and without the table that
// refers to them nothing resolves their digests.
func RemoveCopiedDirectories(blobDir string) ([]string, error) {
	provenance, err := LoadProvenance(blobDir)
	if err != nil {
		return nil, err
	}
	paths := provenance.DirectoryPaths()
	for _, path := range paths {
		if err := removeCopiedDirectory(blobDir, path); err != nil {
			return nil, err
		}
	}
	if err := os.Remove(filepath.Join(blobDir, ProvFile)); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return paths, nil
}

// removeCopiedDirectory deletes the directory at a blob-relative path. A path
// that would reach outside the blob store, which only a hand-edited
// prov.jsonld could carry, is refused rather than followed.
func removeCopiedDirectory(blobDir string, path string) error {
	relative := filepath.FromSlash(strings.TrimSuffix(path, "/"))
	if relative == "" || !filepath.IsLocal(relative) {
		return fmt.Errorf("%s records %q, which is not a directory inside the blob store", ProvFile, path)
	}
	if err := os.RemoveAll(filepath.Join(blobDir, relative)); err != nil {
		return fmt.Errorf("remove the copy of %s: %w", path, err)
	}
	return nil
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
