package salmodule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	rdflibgo "github.com/tggo/goRDFlib"
)

// fileIRIPrefix heads the IRI a task uses to name a file inside its container
// that should be copied out. The path is absolute, so the IRI has no host.
const fileIRIPrefix = "file:///"

// the predicates a copied file is described with, the same ones a pinned
// vocabulary's provenance node carries
const (
	rdfsLabelIRI       = "http://www.w3.org/2000/01/rdf-schema#label"
	dctermsModifiedIRI = "http://purl.org/dc/terms/modified"
	owlVersionIRI      = "http://www.w3.org/2002/07/owl#versionIRI"
)

// CopiedFile is a file a task named with a file:/// IRI in its output and that
// was copied out of the container into the blob store, named by its digest so
// that it is content addressable.
type CopiedFile struct {
	// ContainerPath is the absolute path the file had inside the container.
	ContainerPath string
	// Name is the file name at the end of ContainerPath, recorded as rdfs:label.
	Name string
	// Modified is when the file was last written inside the container,
	// recorded as dcterms:modified.
	Modified time.Time
	// Digest is the hex SHA-256 of the file's contents, which is also its file
	// name in the blob store.
	Digest string
	// Path is where the copy landed on disk.
	Path string
}

// IRI returns the identifier the file's triples use in place of the file:///
// IRI the task wrote, urn:sha256:<digest>, which is the same form the pinned
// vocabulary documents in the blob store are named with.
func (f CopiedFile) IRI() string { return "urn:sha256:" + f.Digest }

// containerFilePath returns the absolute path inside the container that a
// file:/// IRI names. Any other file IRI, one with a host or a relative path,
// is an error, since there is nothing to copy it from.
func containerFilePath(iri string) (string, error) {
	parsed, err := url.Parse(iri)
	if err != nil || parsed.Scheme != "file" {
		return "", fmt.Errorf("%s is not a file:/// IRI", iri)
	}
	if parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") {
		return "", fmt.Errorf("%s does not name an absolute path inside the container; a file to copy is written as file:///absolute/path", iri)
	}
	return path.Clean(parsed.Path), nil
}

// fileCopier copies the files a task names with file:/// IRIs out of the
// container as the task's output arrives, each in its own goroutine so that
// copying overlaps with the task still writing, and finishes them all before
// the container is let go. A file is copied at most once; a later reference to
// it is warned about and skipped.
type fileCopier struct {
	dir   string
	files ContainerFiles

	mu     sync.Mutex
	wg     sync.WaitGroup
	copied map[string]*CopiedFile
	errs   []error
}

// observe scans one line of a task's output for file:/// IRIs and starts
// copying any it has not seen. Every string value is looked at, whichever key
// it sits under, since the line is plain JSON whose coercions are only known
// once the module's @context is applied; a value that turns out not to be an
// IRI object is discarded again when the graph is linked.
func (c *fileCopier) observe(ctx context.Context, line []byte) {
	var node any
	if err := json.Unmarshal(line, &node); err != nil {
		// an unparseable line is reported by GraphFromTaskOutput with its line number
		return
	}
	for _, iri := range fileIRIs(node) {
		containerPath, err := containerFilePath(iri)
		if err != nil {
			// reported when the graph is linked, where it is known whether the
			// value is an IRI object at all
			continue
		}
		c.start(ctx, containerPath)
	}
}

// fileIRIs collects every string value in a decoded JSON value that starts
// with file:///.
func fileIRIs(value any) []string {
	var iris []string
	switch v := value.(type) {
	case string:
		if strings.HasPrefix(v, fileIRIPrefix) {
			iris = append(iris, v)
		}
	case []any:
		for _, item := range v {
			iris = append(iris, fileIRIs(item)...)
		}
	case map[string]any:
		for _, item := range v {
			iris = append(iris, fileIRIs(item)...)
		}
	}
	return iris
}

// start begins copying containerPath unless it already has, in which case the
// repeated reference is warned about.
func (c *fileCopier) start(ctx context.Context, containerPath string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, seen := c.copied[containerPath]; seen {
		slog.Warn("The SAL module references " + fileIRIPrefix + strings.TrimPrefix(containerPath, "/") + " more than once; it was already copied and is not copied again")
		return
	}
	if c.copied == nil {
		c.copied = map[string]*CopiedFile{}
	}
	copied := &CopiedFile{ContainerPath: containerPath, Name: path.Base(containerPath)}
	c.copied[containerPath] = copied

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		err := c.copy(ctx, copied)
		c.mu.Lock()
		defer c.mu.Unlock()
		if err != nil {
			c.errs = append(c.errs, err)
		}
	}()
}

// copy streams one file out of the container into the blob directory, hashing
// it on the way, and names the result by its digest once it is complete so a
// partially copied file is never left under a content addressed name.
func (c *fileCopier) copy(ctx context.Context, copied *CopiedFile) error {
	if err := os.MkdirAll(c.dir, 0755); err != nil {
		return fmt.Errorf("create blob directory for copied files: %w", err)
	}
	temp, err := os.CreateTemp(c.dir, ".copying-*")
	if err != nil {
		return fmt.Errorf("create file for copy of %s: %w", copied.ContainerPath, err)
	}
	defer func() {
		// the temporary file only still exists when the copy failed
		_ = os.Remove(temp.Name())
	}()

	hash := sha256.New()
	modified, copyErr := c.files.CopyFile(ctx, copied.ContainerPath, io.MultiWriter(temp, hash))
	if err := temp.Close(); err != nil && copyErr == nil {
		copyErr = fmt.Errorf("write copy of %s: %w", copied.ContainerPath, err)
	}
	if copyErr != nil {
		return copyErr
	}

	// a container that does not report when the file was written leaves the
	// copy itself as the best known time
	if modified.IsZero() {
		modified = time.Now()
	}
	copied.Modified = modified.UTC()
	copied.Digest = hex.EncodeToString(hash.Sum(nil))
	copied.Path = filepath.Join(c.dir, copied.Digest)
	if err := os.Rename(temp.Name(), copied.Path); err != nil {
		return fmt.Errorf("store copy of %s: %w", copied.ContainerPath, err)
	}
	return nil
}

// wait blocks until every copy has finished and returns the files copied,
// sorted by their path inside the container, or the errors of any that failed.
func (c *fileCopier) wait() ([]CopiedFile, error) {
	c.wg.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.errs) > 0 {
		return nil, errors.Join(c.errs...)
	}
	files := make([]CopiedFile, 0, len(c.copied))
	for _, copied := range c.copied {
		files = append(files, *copied)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].ContainerPath < files[j].ContainerPath })
	return files, nil
}

// LinkCopiedFiles rewrites every file:/// object in a task's graph to the
// urn:sha256: IRI of the copy that was made of it and describes each copy the
// way a pinned vocabulary's provenance node is described: its file name as
// rdfs:label, when it was written as dcterms:modified, and its digest as
// owl:versionIRI. The data product then refers to the copies it holds rather
// than to paths inside a container that no longer exists. A file:// object
// that was not copied is an error. A copy nothing refers to as an IRI object,
// because the task wrote the path as a literal, is removed again and warned
// about.
func LinkCopiedFiles(graph *rdflibgo.Graph, files []CopiedFile) error {
	byPath := map[string]CopiedFile{}
	for _, file := range files {
		byPath[file.ContainerPath] = file
	}

	var fileTriples []rdflibgo.Triple
	graph.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		if object, ok := triple.Object.(rdflibgo.URIRef); ok && strings.HasPrefix(object.Value(), "file:") {
			fileTriples = append(fileTriples, triple)
		}
		return true
	})

	referenced := map[string]bool{}
	for _, triple := range fileTriples {
		iri := triple.Object.(rdflibgo.URIRef).Value()
		containerPath, err := containerFilePath(iri)
		if err != nil {
			return err
		}
		file, ok := byPath[containerPath]
		if !ok {
			return fmt.Errorf("%s was not copied from the container", iri)
		}
		referenced[file.Digest] = true
		copy := rdflibgo.NewURIRefUnsafe(file.IRI())
		graph.Remove(triple.Subject, &triple.Predicate, triple.Object)
		graph.Add(triple.Subject, triple.Predicate, copy)
		graph.Add(copy, rdflibgo.NewURIRefUnsafe(rdfsLabelIRI), rdflibgo.NewLiteral(file.Name))
		graph.Add(copy, rdflibgo.NewURIRefUnsafe(dctermsModifiedIRI), rdflibgo.NewLiteral(file.Modified.Format(time.RFC3339), rdflibgo.WithDatatype(rdflibgo.XSDDateTime)))
		graph.Add(copy, rdflibgo.NewURIRefUnsafe(owlVersionIRI), copy)
	}

	for _, file := range files {
		if referenced[file.Digest] {
			continue
		}
		slog.Warn(fileIRIPrefix + strings.TrimPrefix(file.ContainerPath, "/") + " was copied but nothing in the SAL module's output refers to it as an IRI object, so the copy was discarded; write it as {\"@id\": \"file:///...\"} to keep it")
		if err := os.Remove(file.Path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("discard unreferenced copy of %s: %w", file.ContainerPath, err)
		}
	}
	return nil
}
