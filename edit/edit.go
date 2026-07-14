package edit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/cgs-earth/sal/pkg"
	"github.com/twmb/avro/ocf"
)

type EditCmd struct {
	NewTableRoot string `arg:"--new-table-root" help:"rename the root path of the SAL data product"`
}

func (cfg *EditCmd) Run() error {
	if strings.TrimSpace(cfg.NewTableRoot) == "" {
		return fmt.Errorf("--new-table-root is required")
	}

	dataProductPath, err := pkg.SalBuiltDataProductPath()
	if err != nil {
		return err
	}

	tablePaths, err := IcebergTablePaths(dataProductPath)
	if err != nil {
		return err
	}

	for _, tablePath := range tablePaths {
		changes, err := RewriteIcebergTableRoot(tablePath, cfg.NewTableRoot)
		if err != nil {
			return err
		}
		slog.Info("Updated Iceberg table root",
			"table", tablePath,
			"new_root", normalizeRoot(cfg.NewTableRoot),
			"files_changed", changes,
		)
	}

	return nil
}

// IcebergTablePaths finds Iceberg table directories inside a built SAL data product.
func IcebergTablePaths(dataProductPath string) ([]string, error) {
	if hasMetadataDir(dataProductPath) {
		return []string{dataProductPath}, nil
	}

	entries, err := os.ReadDir(dataProductPath)
	if err != nil {
		return nil, fmt.Errorf("read SAL data product %s: %w", dataProductPath, err)
	}

	var tables []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(dataProductPath, entry.Name())
		if hasMetadataDir(candidate) {
			tables = append(tables, candidate)
		}
	}
	if len(tables) == 0 {
		return nil, fmt.Errorf("no Iceberg table metadata found in %s", dataProductPath)
	}

	return tables, nil
}

func hasMetadataDir(tablePath string) bool {
	info, err := os.Stat(filepath.Join(tablePath, "metadata"))
	return err == nil && info.IsDir()
}

// RewriteIcebergTableRoot updates Iceberg metadata files so a copied table can be read from newRoot.
func RewriteIcebergTableRoot(tablePath string, newRoot string) (int, error) {
	metadataDir := filepath.Join(tablePath, "metadata")
	oldRoot, err := currentTableRoot(tablePath)
	if err != nil {
		return 0, err
	}

	rewrite := rootRewriter{
		oldRoot:       normalizeRoot(oldRoot),
		newRoot:       normalizeRoot(newRoot),
		escapeURIPath: shouldEscapeURIPath(newRoot),
	}
	if rewrite.oldRoot == rewrite.newRoot {
		return 0, nil
	}

	changedFiles, err := rewriteIcebergMetadataFiles(metadataDir, rewrite)
	if err != nil {
		return 0, fmt.Errorf("rewrite Iceberg metadata in %s: %w", metadataDir, err)
	}

	return changedFiles, nil
}

// RewriteIcebergMetadataPath updates references to one metadata file path inside Iceberg metadata files.
func RewriteIcebergMetadataPath(tablePath string, oldPath string, newPath string) (int, error) {
	metadataDir := filepath.Join(tablePath, "metadata")
	rewrite := rootRewriter{
		oldRoot:       normalizeRoot(oldPath),
		newRoot:       normalizeRoot(newPath),
		escapeURIPath: shouldEscapeURIPath(newPath),
	}
	if rewrite.oldRoot == rewrite.newRoot {
		return 0, nil
	}

	changedFiles, err := rewriteIcebergMetadataFiles(metadataDir, rewrite)
	if err != nil {
		return 0, fmt.Errorf("rewrite Iceberg metadata in %s: %w", metadataDir, err)
	}

	return changedFiles, nil
}

func rewriteIcebergMetadataFiles(metadataDir string, rewrite rootRewriter) (int, error) {
	var changedFiles int
	err := filepath.WalkDir(metadataDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}

		var (
			changed bool
			err     error
		)
		switch {
		case strings.HasSuffix(entry.Name(), ".metadata.json"):
			changed, err = rewriteJSONFile(path, rewrite)
		case strings.HasSuffix(entry.Name(), ".avro"):
			changed, err = rewriteAvroFile(path, rewrite)
		default:
			return nil
		}
		if err != nil {
			return err
		}
		if changed {
			changedFiles++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	return changedFiles, nil
}

func currentTableRoot(tablePath string) (string, error) {
	metadataFiles, err := filepath.Glob(filepath.Join(tablePath, "metadata", "*.metadata.json"))
	if err != nil {
		return "", err
	}
	if len(metadataFiles) == 0 {
		return "", fmt.Errorf("no Iceberg metadata JSON files found in %s", tablePath)
	}

	latest := metadataFiles[len(metadataFiles)-1]
	for _, path := range metadataFiles {
		if metadataVersion(path) > metadataVersion(latest) {
			latest = path
		}
	}

	b, err := os.ReadFile(latest)
	if err != nil {
		return "", err
	}
	var metadata map[string]any
	if err := decodeJSON(b, &metadata); err != nil {
		return "", fmt.Errorf("parse %s: %w", latest, err)
	}
	if location, ok := metadata["location"].(string); ok && location != "" {
		return location, nil
	}

	abs, err := filepath.Abs(tablePath)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func metadataVersion(path string) int {
	name := filepath.Base(path)
	if !strings.HasPrefix(name, "v") {
		return -1
	}
	name = strings.TrimSuffix(strings.TrimPrefix(name, "v"), ".metadata.json")
	var version int
	_, _ = fmt.Sscanf(name, "%d", &version)
	return version
}

type rootRewriter struct {
	oldRoot       string
	newRoot       string
	escapeURIPath bool
}

func (r rootRewriter) rewriteString(value string) (string, bool) {
	normalized := normalizeRoot(value)
	if normalized == r.oldRoot {
		return r.newRoot, true
	}
	if strings.HasPrefix(value, r.oldRoot+"/") {
		suffix := strings.TrimPrefix(value, r.oldRoot)
		if r.escapeURIPath {
			suffix = escapeURIPathSuffix(suffix)
		}
		return r.newRoot + suffix, true
	}
	return value, false
}

func shouldEscapeURIPath(root string) bool {
	u, err := url.Parse(root)
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

func escapeURIPathSuffix(path string) string {
	u := url.URL{Path: path}
	return u.EscapedPath()
}

func normalizeRoot(root string) string {
	root = strings.TrimSpace(root)
	for len(root) > len("s3://") && strings.HasSuffix(root, "/") {
		root = strings.TrimSuffix(root, "/")
	}
	return filepath.ToSlash(root)
}

func rewriteJSONFile(path string, rewrite rootRewriter) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	var value any
	if err := decodeJSON(b, &value); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}

	value, changed := rewriteAny(value, rewrite)
	if !changed {
		return false, nil
	}

	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return false, err
	}
	out = append(out, '\n')
	return true, writeFileAtomically(path, out)
}

func decodeJSON(b []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	return decoder.Decode(value)
}

func rewriteAny(value any, rewrite rootRewriter) (any, bool) {
	switch typed := value.(type) {
	case string:
		return rewrite.rewriteString(typed)
	case []any:
		changed := false
		for i, item := range typed {
			rewritten, itemChanged := rewriteAny(item, rewrite)
			typed[i] = rewritten
			changed = changed || itemChanged
		}
		return typed, changed
	case map[string]any:
		changed := false
		for key, item := range typed {
			rewritten, itemChanged := rewriteAny(item, rewrite)
			typed[key] = rewritten
			changed = changed || itemChanged
		}
		return typed, changed
	default:
		return value, false
	}
}

func rewriteAvroFile(path string, rewrite rootRewriter) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	reader, err := ocf.NewReader(bytes.NewReader(b))
	if err != nil {
		return false, fmt.Errorf("open Avro OCF %s: %w", path, err)
	}
	defer func() { _ = reader.Close() }()

	var records []any
	changed := false
	for {
		var record any
		err := reader.Decode(&record)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return false, fmt.Errorf("decode Avro OCF %s: %w", path, err)
		}
		record, recordChanged := rewriteAny(record, rewrite)
		records = append(records, record)
		changed = changed || recordChanged
	}
	if !changed {
		return false, nil
	}

	var out bytes.Buffer
	options := []ocf.WriterOpt{ocf.WithSchema(string(reader.Metadata()["avro.schema"]))}
	if codec, ok, err := avroCodec(reader.Metadata()); err != nil {
		return false, err
	} else if ok {
		options = append(options, ocf.WithCodec(codec))
	}
	if metadata := avroUserMetadata(reader.Metadata()); len(metadata) > 0 {
		options = append(options, ocf.WithMetadata(metadata))
	}

	writer, err := ocf.NewWriter(&out, reader.Schema(), options...)
	if err != nil {
		return false, fmt.Errorf("create Avro OCF writer for %s: %w", path, err)
	}
	for _, record := range records {
		if err := writer.Encode(record); err != nil {
			return false, fmt.Errorf("encode Avro OCF %s: %w", path, err)
		}
	}
	if err := writer.Close(); err != nil {
		return false, fmt.Errorf("close Avro OCF writer for %s: %w", path, err)
	}

	return true, writeFileAtomically(path, out.Bytes())
}

func avroCodec(metadata map[string][]byte) (ocf.Codec, bool, error) {
	name := string(metadata["avro.codec"])
	switch name {
	case "", "null":
		return nil, false, nil
	case "deflate":
		return ocf.DeflateCodec(-1), true, nil
	case "snappy":
		return ocf.SnappyCodec(), true, nil
	case "zstandard":
		codec, err := ocf.ZstdCodec(nil, nil)
		return codec, err == nil, err
	default:
		return nil, false, fmt.Errorf("unsupported Avro OCF codec %q", name)
	}
}

func avroUserMetadata(metadata map[string][]byte) map[string][]byte {
	userMetadata := make(map[string][]byte)
	for key, value := range metadata {
		if strings.HasPrefix(key, "avro.") {
			continue
		}
		userMetadata[key] = value
	}
	return userMetadata
}

func writeFileAtomically(path string, contents []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(contents); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, info.Mode()); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
