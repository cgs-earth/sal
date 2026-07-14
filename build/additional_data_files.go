package build

import (
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/cgs-earth/sal/pkg"
	rdflibgo "github.com/tggo/goRDFlib"
)

const (
	schemaNamespace = "https://schema.org/"
	dcatNamespace   = "http://www.w3.org/ns/dcat#"
	dctNamespace    = "http://purl.org/dc/terms/"
)

var additionalFileMIMETypes = map[string]string{
	".fgb":     "application/vnd.flatgeobuf",
	".pmtiles": "application/vnd.pmtiles",
}

// AddAdditionalDataFileMetadata adds DCAT metadata for extra top-level files in .sal/data.
func AddAdditionalDataFileMetadata(graph *rdflibgo.Graph) error {
	dataDir, err := pkg.SalDataDir()
	if err != nil {
		return err
	}
	projectName, err := pkg.GitProjectName()
	if err != nil {
		return err
	}
	base, err := pkg.DefaultSalBase()
	if err != nil {
		return err
	}
	return addAdditionalDataFileMetadata(graph, dataDir, projectName, base)
}

func addAdditionalDataFileMetadata(graph *rdflibgo.Graph, dataDir string, projectName string, base string) error {
	entries, err := os.ReadDir(dataDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read SAL data directory %s: %w", dataDir, err)
	}

	graph.Bind("schema", rdflibgo.NewURIRefUnsafe(schemaNamespace))
	graph.Bind("dcat", rdflibgo.NewURIRefUnsafe(dcatNamespace))
	graph.Bind("dct", rdflibgo.NewURIRefUnsafe(dctNamespace))

	for _, entry := range entries {
		if !additionalDataFileEntry(entry, projectName) {
			continue
		}

		subject := rdflibgo.NewURIRefUnsafe(entry.Name())
		graph.Add(subject, rdflibgo.RDF.Type, rdflibgo.NewURIRefUnsafe(dcatNamespace+"Dataset"))
		graph.Add(subject, rdflibgo.NewURIRefUnsafe(schemaNamespace+"name"), rdflibgo.NewLiteral(entry.Name()))
		graph.Add(subject, rdflibgo.NewURIRefUnsafe(dctNamespace+"format"), rdflibgo.NewLiteral(mimeTypeForAdditionalDataFile(entry.Name())))
		graph.Add(subject, rdflibgo.NewURIRefUnsafe(dcatNamespace+"downloadURL"), subject)
		graph.Add(subject, rdflibgo.NewURIRefUnsafe(schemaNamespace+"isAccessibleForFree"), rdflibgo.NewLiteral(true))
	}
	return nil
}

func additionalDataFileEntry(entry os.DirEntry, projectName string) bool {
	if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
		return false
	}
	name := entry.Name()
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	return name != projectName && stem != projectName
}

func mimeTypeForAdditionalDataFile(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if mediaType := additionalFileMIMETypes[ext]; mediaType != "" {
		return mediaType
	}
	mediaType := mime.TypeByExtension(ext)
	if mediaType == "" {
		return "application/octet-stream"
	}
	if parsed, _, err := mime.ParseMediaType(mediaType); err == nil {
		return parsed
	}
	return mediaType
}
