package validate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cgs-earth/sal/pkg"
	"golang.org/x/sync/errgroup"
)

// SeedFile is the name, under the blob directory, of the index of the
// vocabulary documents `sal init` seeded: a JSON-LD document in the shape
// .sal/config.jsonld records pins in, so each seeded namespace is named
// against the digest of the document stored beside it.
//
// A seed is not a pin. Pins are the vocabularies a project resolves against
// and sit in .sal/config.jsonld, which is committed; seeds are a local
// offline cache of widely used vocabularies, so that a build that uses one
// needs no network and a flaky vocabulary host cannot fail it. A seed becomes
// a pin only when a build actually uses its namespace.
const SeedFile = "seed.jsonld"

// CommonVocabularies are the namespaces `sal init` seeds: the W3C core
// vocabularies, Dublin Core, schema.org, and the sensor, units, geospatial and
// catalog vocabularies data products commonly state themselves in.
var CommonVocabularies = []string{
	"http://www.w3.org/1999/02/22-rdf-syntax-ns#",
	"http://www.w3.org/2000/01/rdf-schema#",
	"http://www.w3.org/2002/07/owl#",
	"http://purl.org/dc/terms/",
	"http://purl.org/dc/elements/1.1/",
	"https://schema.org/",
	"http://www.w3.org/ns/sosa/",
	"http://www.w3.org/ns/ssn/",
	"http://qudt.org/schema/qudt/",
	"http://www.w3.org/2004/02/skos/core#",
	"http://xmlns.com/foaf/0.1/",
	"http://www.w3.org/ns/prov#",
	"http://www.w3.org/ns/dcat#",
	"http://www.opengis.net/ont/geosparql#",
	"http://www.w3.org/2006/time#",
	"http://www.w3.org/ns/shacl#",
}

// SeedFetch dereferences a vocabulary document for SeedVocabularies. Tests
// replace it.
var SeedFetch = fetchVocabularyDocument

const (
	seedConcurrency = 4
	// seedTimeout bounds each vocabulary, retries included, so that seeding
	// from a network that cannot reach the publishers costs about
	// len(CommonVocabularies)/seedConcurrency timeouts rather than minutes each
	seedTimeout = 30 * time.Second
)

// SeedVocabularies stores the document of every CommonVocabularies namespace
// under blobDir, named by its SHA-256 like a pinned document, and records them
// in blobDir/SeedFile. It writes no pin, so nothing is added to the project's
// vocabularies until a build uses one. A namespace already seeded whose
// document is still on disk is left alone, so it is safe to run again; one
// that cannot be fetched or does not parse as RDF is warned about and skipped
// rather than failing the rest. It returns the namespaces now seeded.
func SeedVocabularies(ctx context.Context, blobDir string) ([]string, error) {
	if err := os.MkdirAll(blobDir, 0755); err != nil {
		return nil, err
	}
	seedPath := filepath.Join(blobDir, SeedFile)
	seeded := loadSeeds(seedPath)

	var (
		mu    sync.Mutex
		group errgroup.Group
	)
	group.SetLimit(seedConcurrency)
	for _, namespace := range CommonVocabularies {
		if entry, ok := seeded[namespace]; ok {
			if _, err := os.Stat(filepath.Join(blobDir, entry.Version)); err == nil {
				continue
			}
		}
		group.Go(func() error {
			entry, err := seedOne(ctx, blobDir, namespace)
			if err != nil {
				slog.Warn("could not seed a common vocabulary; a build will fetch it when it is used", "vocabulary", namespace, "error", err)
				return nil
			}
			mu.Lock()
			seeded[namespace] = entry
			mu.Unlock()
			return nil
		})
	}
	_ = group.Wait()

	nodes, err := (&PinnedVocabularies{entries: seeded}).nodes()
	if err != nil {
		return nil, err
	}
	doc := &pkg.ConfigDocument{Context: pkg.SalConfigContext}
	for _, node := range nodes {
		doc.Graph = append(doc.Graph, json.RawMessage(node))
	}
	if err := pkg.WriteConfigDocument(seedPath, doc); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(seeded))
	for namespace := range seeded {
		names = append(names, namespace)
	}
	sort.Strings(names)
	return names, nil
}

// seedOne fetches one vocabulary, checks that SAL can read it, and stores it
// under its digest.
func seedOne(ctx context.Context, blobDir string, namespace string) (pinnedVocabulary, error) {
	ctx, cancel := context.WithTimeout(ctx, seedTimeout)
	defer cancel()
	body, mediaType, _, err := SeedFetch(ctx, vocabularyDocumentURL(namespace))
	if err != nil {
		return pinnedVocabulary{}, err
	}
	terms, _, err := serializeRdfDataAndGetVocab(mediaType, body, namespace)
	if err != nil {
		return pinnedVocabulary{}, err
	}
	// a page that is not the vocabulary (an HTML error page, say) can parse as
	// RDF with nothing in it, so the document must define a term of its own
	// namespace
	defines := false
	for term := range terms {
		if len(term) > len(namespace) && strings.HasPrefix(term, namespace) {
			defines = true
			break
		}
	}
	if !defines {
		return pinnedVocabulary{}, fmt.Errorf("the document fetched for %s defines no terms in that namespace", namespace)
	}
	digest := documentDigest(body)
	path := filepath.Join(blobDir, digest)
	if _, err := os.Stat(path); err != nil {
		if err := os.WriteFile(path, body, 0644); err != nil {
			return pinnedVocabulary{}, fmt.Errorf("store %s: %w", namespace, err)
		}
	}
	return pinnedVocabulary{Scheme: sha256VersionScheme, Version: digest, MediaType: mediaType, Modified: time.Now().UTC()}, nil
}

// loadSeeds reads the seed index, or an empty one when there is none or it
// cannot be read: a seed is only a cache.
func loadSeeds(path string) map[string]pinnedVocabulary {
	seeds := &PinnedVocabularies{entries: map[string]pinnedVocabulary{}}
	doc, err := pkg.ReadConfigDocument(path)
	if err == nil {
		var nodes []json.RawMessage
		if _, nodes, err = pkg.PartitionConfigGraph(doc.Graph); err == nil {
			err = seeds.unmarshal(nodes)
		}
	}
	if err != nil {
		slog.Warn("ignoring an unreadable vocabulary seed index", "path", path, "error", err)
		return map[string]pinnedVocabulary{}
	}
	return seeds.entries
}
