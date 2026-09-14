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

// provNode is one copied directory in prov.jsonld. Its @id is the directory's
// path relative to the blob store, ending in a slash, so that it resolves
// against wherever the document is served from, /blobs/ or a bucket, to the
// directory itself.
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

// DirectoryPath returns the path, relative to the blob store and ending in a
// slash, of the directory whose contents hash to digest, given bare or as
// urn:sha256:<digest>, and whether there is one.
func (p *Provenance) DirectoryPath(digest string) (string, bool) {
	versionIRI := "urn:sha256:" + strings.TrimPrefix(digest, "urn:sha256:")
	for _, node := range p.nodes {
		if node.VersionIRI.ID == versionIRI {
			return node.ID, true
		}
	}
	return "", false
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
			ID:         directory.BlobPath,
			Label:      directory.Name,
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
	sort.Slice(provenance.nodes, func(i, j int) bool { return provenance.nodes[i].ID < provenance.nodes[j].ID })

	content, err := json.MarshalIndent(provDocument{Context: provContext, Graph: provenance.nodes}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(blobDir, ProvFile), append(content, '\n'), 0644)
}
