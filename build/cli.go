package build

import (
	"fmt"
	"strings"
)

func prefixMappedVocabularyFetch(entries []string, fetch func(string) ([]byte, string, error)) (func(string) ([]byte, string, error), error) {
	mappings, err := parsePrefixMaps(entries)
	if err != nil {
		return nil, err
	}

	return func(u string) ([]byte, string, error) {
		if mapped, ok := mappings[vocabularyDocumentURL(u)]; ok {
			u = mapped
		}
		return fetch(u)
	}, nil
}

func parsePrefixMaps(entries []string) (map[string]string, error) {
	mappings := map[string]string{}
	for i := 0; i < len(entries); {
		entry := entries[i]
		if key, value, ok := strings.Cut(entry, "="); ok {
			if key == "" || value == "" {
				return nil, fmt.Errorf("invalid prefix map %q: expected source target or source=target", entry)
			}
			mappings[vocabularyDocumentURL(key)] = value
			i++
			continue
		}
		if i+1 >= len(entries) {
			return nil, fmt.Errorf("invalid prefix map %q: expected source target or source=target", entry)
		}
		mappings[vocabularyDocumentURL(entry)] = entries[i+1]
		i += 2
	}
	return mappings, nil
}
