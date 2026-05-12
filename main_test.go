package main

import (
	"strings"
	"testing"
)

func TestParseNQuadLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want triple
	}{
		{
			name: "iri object",
			line: `<http://example.com/s> <http://example.com/p> <http://example.com/o> <http://example.com/g> .`,
			want: triple{s: "http://example.com/s", p: "http://example.com/p", o: "http://example.com/o"},
		},
		{
			name: "blank subject literal object",
			line: `_:b1 <http://example.com/p> "hello\nworld"@en . # trailing comment`,
			want: triple{s: "b1", p: "http://example.com/p", o: "hello\nworld"},
		},
		{
			name: "typed literal",
			line: `<http://example.com/s> <http://example.com/p> "42"^^<http://www.w3.org/2001/XMLSchema#integer> .`,
			want: triple{s: "http://example.com/s", p: "http://example.com/p", o: "42"},
		},
		{
			name: "escaped iri",
			line: `<http://example.com/\u0073> <http://example.com/p> _:obj .`,
			want: triple{s: "http://example.com/s", p: "http://example.com/p", o: "obj"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseNQuadLine(tt.line)
			if err != nil {
				t.Fatalf("parseNQuadLine() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("parseNQuadLine() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseNQuadsSkipsBadLines(t *testing.T) {
	input := strings.Join([]string{
		`<http://example.com/s1> <http://example.com/p> "ok" .`,
		`not valid`,
		`<http://example.com/s2> <http://example.com/p> <http://example.com/o> <http://example.com/g> .`,
	}, "\n")

	var got []triple
	if err := parseNQuads(strings.NewReader(input), func(t triple) error {
		got = append(got, t)
		return nil
	}); err != nil {
		t.Fatalf("parseNQuads() error = %v", err)
	}

	want := []triple{
		{s: "http://example.com/s1", p: "http://example.com/p", o: "ok"},
		{s: "http://example.com/s2", p: "http://example.com/p", o: "http://example.com/o"},
	}
	if len(got) != len(want) {
		t.Fatalf("parseNQuads() parsed %d triples, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseNQuads()[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}
