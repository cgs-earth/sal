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

	return hex.EncodeToString(h.Sum(nil)), nil
}
