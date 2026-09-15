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
// that should be copied out. The path is absolute, so the IRI has no host. A
// path ending in a slash names a directory, copied whole.
const fileIRIPrefix = "file:///"

// the predicates a copied file is described with: the ones a pinned
// vocabulary's provenance node carries, plus the path the copy sits at under
// the blob store
const (
	rdfsLabelIRI         = "http://www.w3.org/2000/01/rdf-schema#label"
	rdfsCommentIRI       = "http://www.w3.org/2000/01/rdf-schema#comment"
	dctermsModifiedIRI   = "http://purl.org/dc/terms/modified"
	dctermsIdentifierIRI = "http://purl.org/dc/terms/identifier"
	owlVersionIRI        = "http://www.w3.org/2002/07/owl#versionIRI"
)

// CopiedFile is a file or directory a task named with a file:/// IRI in its
// output and that was copied out of the container into the blob store. A file
// is stored under its digest so that it is content addressable; a directory is
// stored verbatim under the path it had in the container, so that whatever
// reads it, a Zarr or STAC client for instance, finds the layout it expects,
// and its digest is recorded in the blob store's prov.jsonld instead.
type CopiedFile struct {
	// ContainerPath is the absolute path the file had inside the container,
	// ending in a slash for a directory.
	ContainerPath string
	// Directory reports whether the copy is a whole directory rather than one
	// file.
	Directory bool
	// Name is the file or directory name at the end of ContainerPath,
	// recorded as rdfs:label. A directory's name keeps its trailing slash,
	// which is what tells the two apart wherever the label is read.
	Name string
	// Modified is when the file was last written inside the container, or the
	// newest such time of any file in a directory, recorded as
	// dcterms:modified.
	Modified time.Time
	// Digest is the hex SHA-256 of the file's contents, or of a directory's
	// tree (see treeDigest). It names a file in the blob store.
	Digest string
	// BlobPath is where the copy sits relative to the blob store, recorded as
	// dcterms:identifier: the digest for a file, the container path without
	// its leading slash for a directory. It is also the path the copy is
	// served at under /blobs/.
	BlobPath string
	// Path is where the copy landed on disk.
	Path string
}

// IRI returns the identifier the file's triples use in place of the file:///
// IRI the task wrote, urn:sha256:<digest>, which is the same form the pinned
// vocabulary documents in the blob store are named with.
func (f CopiedFile) IRI() string { return "urn:sha256:" + f.Digest }

// containerFilePath returns the absolute path inside the container that a
// file:/// IRI names, and whether it names a directory, which a trailing slash
// marks. Any other file IRI, one with a host or a relative path, is an error,
// since there is nothing to copy it from.
func containerFilePath(iri string) (containerPath string, isDir bool, err error) {
	parsed, err := url.Parse(iri)
	if err != nil || parsed.Scheme != "file" {
		return "", false, fmt.Errorf("%s is not a file:/// IRI", iri)
	}
	if parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") {
		return "", false, fmt.Errorf("%s does not name an absolute path inside the container; a file to copy is written as file:///absolute/path, a directory as file:///absolute/path/", iri)
	}
	isDir = strings.HasSuffix(parsed.Path, "/")
	containerPath = path.Clean(parsed.Path)
	if isDir {
		if containerPath == "/" {
			return "", false, fmt.Errorf("%s names the root of the container, which cannot be copied", iri)
		}
		containerPath += "/"
	}
	return containerPath, isDir, nil
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
// IRI at all is discarded again when the graph is linked.
func (c *fileCopier) observe(ctx context.Context, line []byte) {
	var node any
	if err := json.Unmarshal(line, &node); err != nil {
		// an unparseable line is reported by GraphFromTaskOutput with its line number
		return
	}
	for _, iri := range fileIRIs(node) {
		containerPath, isDir, err := containerFilePath(iri)
		if err != nil {
			// reported when the graph is linked, where it is known whether the
			// value is an IRI at all
			continue
		}
		c.start(ctx, containerPath, isDir)
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
func (c *fileCopier) start(ctx context.Context, containerPath string, isDir bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, seen := c.copied[containerPath]; seen {
		slog.Warn("The SAL module references " + fileIRIPrefix + strings.TrimPrefix(containerPath, "/") + " more than once; it was already copied and is not copied again")
		return
	}
	if c.copied == nil {
		c.copied = map[string]*CopiedFile{}
	}
	name := path.Base(containerPath)
	if isDir {
		name += "/"
	}
	copied := &CopiedFile{ContainerPath: containerPath, Directory: isDir, Name: name}
	c.copied[containerPath] = copied

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		var err error
		if isDir {
			err = c.copyDirectory(ctx, copied)
		} else {
			err = c.copyFile(ctx, copied)
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if err != nil {
			c.errs = append(c.errs, err)
		}
	}()
}

// copyFile streams one file out of the container into the blob directory,
// hashing it on the way, and names the result by its digest once it is
// complete so a partially copied file is never left under a content addressed
// name.
func (c *fileCopier) copyFile(ctx context.Context, copied *CopiedFile) error {
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
	copied.BlobPath = copied.Digest
	copied.Path = filepath.Join(c.dir, copied.Digest)
	if err := os.Rename(temp.Name(), copied.Path); err != nil {
		return fmt.Errorf("store copy of %s: %w", copied.ContainerPath, err)
	}
	return nil
}

// copyDirectory streams a whole directory out of the container into a
// temporary directory in the blob store, hashing each file on the way, and
// moves it to its place under the container's own path only once every entry
// has arrived, so a partially copied tree is never found where a complete one
// is expected. A directory already there from an earlier copy is replaced,
// since a path names the current copy rather than one version of it; the
// digest is what tells versions apart.
func (c *fileCopier) copyDirectory(ctx context.Context, copied *CopiedFile) error {
	if err := os.MkdirAll(c.dir, 0755); err != nil {
		return fmt.Errorf("create blob directory for copied files: %w", err)
	}
	temp, err := os.MkdirTemp(c.dir, ".copying-*")
	if err != nil {
		return fmt.Errorf("create directory for copy of %s: %w", copied.ContainerPath, err)
	}
	defer func() {
		// the temporary directory only still exists when the copy failed
		_ = os.RemoveAll(temp)
	}()

	var modified time.Time
	var entries []treeEntry
	err = c.files.CopyDirectory(ctx, copied.ContainerPath, func(relative string, isDir bool, entryModified time.Time, content io.Reader) error {
		if !filepath.IsLocal(relative) {
			return fmt.Errorf("copy %s from container: entry %q escapes the directory", copied.ContainerPath, relative)
		}
		destination := filepath.Join(temp, filepath.FromSlash(relative))
		if isDir {
			return os.MkdirAll(destination, 0755)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return err
		}
		file, err := os.Create(destination)
		if err != nil {
			return fmt.Errorf("write copy of %s%s: %w", copied.ContainerPath, relative, err)
		}
		hash := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(file, hash), content)
		if err := file.Close(); err != nil && copyErr == nil {
			copyErr = err
		}
		if copyErr != nil {
			return fmt.Errorf("write copy of %s%s: %w", copied.ContainerPath, relative, copyErr)
		}
		if entryModified.After(modified) {
			modified = entryModified
		}
		entries = append(entries, treeEntry{path: relative, digest: hex.EncodeToString(hash.Sum(nil))})
		return nil
	})
	if err != nil {
		return err
	}

	if modified.IsZero() {
		modified = time.Now()
	}
	copied.Modified = modified.UTC()
	copied.Digest = treeDigest(entries)
	copied.BlobPath = strings.TrimPrefix(copied.ContainerPath, "/")
	copied.Path = filepath.Join(c.dir, filepath.FromSlash(strings.TrimSuffix(copied.BlobPath, "/")))
	if err := os.MkdirAll(filepath.Dir(copied.Path), 0755); err != nil {
		return fmt.Errorf("store copy of %s: %w", copied.ContainerPath, err)
	}
	if err := os.RemoveAll(copied.Path); err != nil {
		return fmt.Errorf("replace the earlier copy of %s: %w", copied.ContainerPath, err)
	}
	if err := os.Rename(temp, copied.Path); err != nil {
		return fmt.Errorf("store copy of %s: %w", copied.ContainerPath, err)
	}
	return nil
}

// treeEntry is one regular file of a copied directory, by its path relative
// to the directory and the digest of its contents.
type treeEntry struct {
	path   string
	digest string
}

// treeDigest is the SHA-256 of a directory's contents: one line per regular
// file, sorted by path, of the path, a NUL, and the file's own SHA-256 in
// hex, the way a git tree object hashes what it holds. It depends only on the
// paths and contents, so a directory copied again unchanged hashes the same
// whatever the modification times or the order the container listed it in.
func treeDigest(entries []treeEntry) string {
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	hash := sha256.New()
	for _, entry := range entries {
		hash.Write([]byte(entry.path + "\x00" + entry.digest + "\n"))
	}
	return hex.EncodeToString(hash.Sum(nil))
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

// LinkCopiedFiles rewrites every file:/// IRI in a task's graph, as subject or
// object, to the urn:sha256: IRI of the copy that was made of it and describes
// each copy the way a pinned vocabulary's provenance node is described: its
// name as rdfs:label, ending in a slash for a directory, when it was written
// as dcterms:modified, and its digest
// as owl:versionIRI, plus where it sits under the blob store as
// dcterms:identifier. Whatever else the task said about the file:/// IRI
// stays with it under its new name. The data product then refers to the
// copies it holds rather than to paths inside a container that no longer
// exists. A file:// IRI that was not copied is an error. A copy nothing
// refers to as an IRI, because the task wrote the path as a literal, is
// removed again and warned about. Every directory that is kept is recorded in
// the blob store's prov.jsonld under blobDir, which is how its digest is
// resolved back to the directory.
func LinkCopiedFiles(graph *rdflibgo.Graph, files []CopiedFile, blobDir string) error {
	byPath := map[string]CopiedFile{}
	for _, file := range files {
		byPath[file.ContainerPath] = file
	}

	var fileTriples []rdflibgo.Triple
	graph.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		if isFileIRI(triple.Subject) || isFileIRI(triple.Object) {
			fileTriples = append(fileTriples, triple)
		}
		return true
	})

	referenced := map[string]bool{}
	link := func(term rdflibgo.Term) (rdflibgo.Term, error) {
		if !isFileIRI(term) {
			return term, nil
		}
		iri := term.(rdflibgo.URIRef).Value()
		containerPath, _, err := containerFilePath(iri)
		if err != nil {
			return nil, err
		}
		file, ok := byPath[containerPath]
		if !ok {
			return nil, fmt.Errorf("%s was not copied from the container", iri)
		}
		copy := rdflibgo.NewURIRefUnsafe(file.IRI())
		if !referenced[containerPath] {
			referenced[containerPath] = true
			graph.Add(copy, rdflibgo.NewURIRefUnsafe(rdfsLabelIRI), rdflibgo.NewLiteral(file.Name))
			graph.Add(copy, rdflibgo.NewURIRefUnsafe(dctermsModifiedIRI), rdflibgo.NewLiteral(file.Modified.Format(time.RFC3339), rdflibgo.WithDatatype(rdflibgo.XSDDateTime)))
			graph.Add(copy, rdflibgo.NewURIRefUnsafe(dctermsIdentifierIRI), rdflibgo.NewLiteral(file.BlobPath))
			graph.Add(copy, rdflibgo.NewURIRefUnsafe(owlVersionIRI), copy)
		}
		return copy, nil
	}
	for _, triple := range fileTriples {
		subject, err := link(triple.Subject)
		if err != nil {
			return err
		}
		object, err := link(triple.Object)
		if err != nil {
			return err
		}
		graph.Remove(triple.Subject, &triple.Predicate, triple.Object)
		graph.Add(subject.(rdflibgo.Subject), triple.Predicate, object)
	}

	var directories []CopiedFile
	for _, file := range files {
		if !referenced[file.ContainerPath] {
			slog.Warn(fileIRIPrefix + strings.TrimPrefix(file.ContainerPath, "/") + " was copied but nothing in the SAL module's output refers to it as an IRI, so the copy was discarded; write it as {\"@id\": \"file:///...\"} to keep it")
			if err := os.RemoveAll(file.Path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("discard unreferenced copy of %s: %w", file.ContainerPath, err)
			}
			continue
		}
		if file.Directory {
			directories = append(directories, file)
		}
	}
	if len(directories) == 0 {
		return nil
	}
	return recordDirectories(blobDir, directories)
}

// isFileIRI reports whether term is an IRI with the file scheme.
func isFileIRI(term rdflibgo.Term) bool {
	iri, ok := term.(rdflibgo.URIRef)
	return ok && strings.HasPrefix(iri.Value(), "file:")
}
