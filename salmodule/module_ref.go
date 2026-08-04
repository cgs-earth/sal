package salmodule

import (
	"fmt"
	"slices"
	"strings"
)

// Constants describing the SAL Module specification. The command line
// conventions mirror the salmodule:baseCommand, salmodule:ontologyCommand,
// salmodule:runCommand, and salmodule:taskInstanceEnvVar ontology properties
// declared in build/testdata/reference/public/salmodule.ttl.
const (
	// Namespace is the IRI of the SAL Module ontology itself.
	Namespace = "https://w3id.org/sal/cgs-earth/sal-module-spec/salmodule#"
	// ProtocolScheme is the URI scheme used to reference a SAL module from RDF.
	ProtocolScheme = "salmodule"

	BaseCommand     = "salmodule"
	OntologyCommand = "ontology"
	RunCommand      = "run"

	// DefaultTaskInstanceEnvVar is used when a module ontology does not declare
	// its own salmodule:taskInstanceEnvVar.
	DefaultTaskInstanceEnvVar = "SALMODULE_TASK_INSTANCE"

	// defaultModuleHost is assumed when a salmodule:// IRI omits the host.
	defaultModuleHost = "github.com"

	// IcebergTableProperty is the Iceberg table property a build records the
	// JSON list of every SAL module it downloaded under.
	IcebergTableProperty = "sal.salmodules"
)

// Task classes defined by the SAL Module ontology. A module declares its own
// classes as subclasses of one of these.
var taskBaseClasses = []string{
	Namespace + "Task",
	Namespace + "NodeProcessor",
	Namespace + "NodeProducer",
	Namespace + "NodeConsumer",
}

// IsTaskBaseClass reports whether iri is one of the SAL Module ontology's own
// task classes.
func IsTaskBaseClass(iri string) bool {
	return slices.Contains(taskBaseClasses, iri)
}

// ModuleRef is a SAL module dereferenced from a salmodule:// IRI.
type ModuleRef struct {
	// Namespace is the vocabulary base that the module's ontology terms resolve
	// against. It always ends in a slash.
	Namespace string
	// CloneURL is the git repository holding the module's Dockerfile.
	CloneURL string
	// ImageTag is the local docker tag SAL builds the module under.
	ImageTag string
}

// IsModuleIRI reports whether iri uses the salmodule protocol scheme.
func IsModuleIRI(iri string) bool {
	return strings.HasPrefix(iri, ProtocolScheme+"://")
}

// ParseModuleIRI resolves a salmodule://[HOST/]OWNER/REPO IRI into the git
// repository that provides the module and the local image tag SAL builds it as.
// Any fragment or term suffix on the IRI is ignored so that both a vocabulary
// base and an individual term IRI resolve to the same module.
func ParseModuleIRI(iri string) (ModuleRef, error) {
	if !IsModuleIRI(iri) {
		return ModuleRef{}, fmt.Errorf("%q is not a %s:// IRI", iri, ProtocolScheme)
	}

	path, _, _ := strings.Cut(strings.TrimPrefix(iri, ProtocolScheme+"://"), "#")
	var segments []string
	for segment := range strings.SplitSeq(path, "/") {
		if segment != "" {
			segments = append(segments, segment)
		}
	}

	host := defaultModuleHost
	// a leading segment containing a dot is a hostname rather than the repo owner
	if len(segments) > 2 && strings.Contains(segments[0], ".") {
		host = segments[0]
		segments = segments[1:]
	}
	if len(segments) < 2 {
		return ModuleRef{}, fmt.Errorf("%q must be of the form %s://[HOST/]OWNER/REPO", iri, ProtocolScheme)
	}
	owner, repo := segments[0], segments[1]

	// segments past OWNER/REPO name a term inside the module vocabulary
	segments = segments[:2]
	return ModuleRef{
		Namespace: fmt.Sprintf("%s://%s/%s/%s/", ProtocolScheme, host, owner, repo),
		CloneURL:  fmt.Sprintf("https://%s/%s/%s.git", host, owner, repo),
		ImageTag:  imageTag(host, owner, repo),
	}, nil
}

// imageTag builds a docker repository name that is unique per module and only
// contains characters a docker tag allows.
func imageTag(host, owner, repo string) string {
	name := strings.ToLower(fmt.Sprintf("sal-module-%s-%s-%s", host, owner, repo))
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, name)
	return sanitized + ":latest"
}
