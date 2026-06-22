// Licensed to the Apache Software Foundation (ASF) under one
// or more contributor license agreements. See the NOTICE file
// distributed with this work for additional information
// regarding copyright ownership. The ASF licenses this file
// to you under the Apache License, Version 2.0 (the
// "License"); you may not use this file except in compliance
// with the License. You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied. See the License for the
// specific language governing permissions and limitations
// under the License.

package vocab

import (
	"embed"
	"fmt"
	"mime"
	"net/url"
	"path/filepath"
	"strings"
)

// Run go generate ./build/vocab to refresh bundled vocabulary documents.
//go:generate bash gen_external_vocabularies.sh

//go:embed *
var files embed.FS

// Load returns a checked-in vocabulary document for URLs that cannot be
// dereferenced directly.
func Load(base string) ([]byte, string, bool, error) {
	name, ok := fileName(base)
	if !ok {
		return nil, "", false, nil
	}

	body, err := files.ReadFile(name)
	if err != nil {
		return nil, "", false, err
	}
	return body, contentType(name), true, nil
}

func fileName(base string) (string, bool) {
	candidates := []string{url.QueryEscape(base)}
	trimmed := strings.TrimRight(base, "/")
	if trimmed != base {
		candidates = append(candidates, url.QueryEscape(trimmed))
	}

	for _, ext := range []string{".jsonld", ".ttl", ".turtle", ".rdf", ".xml"} {
		for _, encoded := range candidates {
			name := encoded + ext
			f, err := files.Open(name)
			if err == nil {
				_ = f.Close()
				return name, true
			}
		}
	}
	return "", false
}

func contentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jsonld":
		return "application/ld+json"
	case ".ttl", ".turtle":
		return "text/turtle"
	case ".rdf", ".xml":
		return "application/rdf+xml"
	default:
		if contentType := mime.TypeByExtension(filepath.Ext(name)); contentType != "" {
			return contentType
		}
		return "application/octet-stream"
	}
}

func MissingError(base string) error {
	return fmt.Errorf("no embedded vocabulary for %s", base)
}
