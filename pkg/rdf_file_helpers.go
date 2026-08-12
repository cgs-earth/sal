package pkg

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func FindRdfDataInPaths(paths []string) ([]string, error) {
	var files []string
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("build: %s: %w", path, err)
		}
		if !info.IsDir() {
			if isRdfData(path) {
				files = append(files, path)
			}
			continue
		}
		err = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// .sal holds generated data and SAL's own project files, not the
			// project's RDF sources; its config.jsonld is read by build directly
			if d.IsDir() && d.Name() == ".sal" && p != path {
				return filepath.SkipDir
			}
			if !d.IsDir() && isRdfData(p) {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("build: walk %s: %w", path, err)
		}
	}
	sort.Strings(files)
	return files, nil
}

func isRdfData(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".jsonld" || ext == ".json" || ext == ".ttl" || ext == ".turtle"
}

func HashAllFiles(paths []string) (string, error) {
	return HashFilesAndContent(paths, nil)
}

// HashFilesAndContent hashes every path's bytes, in order, followed by extra
// in-memory content. build uses it to fold the project ontology's content
// into the same hash HashAllFiles produces for the RDF source files, even
// though the ontology no longer has a file of its own to pass a path for; it
// is read out of .sal/config.jsonld instead.
func HashFilesAndContent(paths []string, extra []byte) (string, error) {
	h := sha256.New()

	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}

		_, err = io.Copy(h, file)
		if err != nil {
			return "", err
		}
		err = file.Close()
		if err != nil {
			return "", err
		}
	}
	if len(extra) > 0 {
		h.Write(extra)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
