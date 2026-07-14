package load

type objectKind int

const (
	objectKindIRI objectKind = iota
	objectKindBNode
	objectKindLiteral
)

type rdfObject struct {
	o         string
	oKind     objectKind
	oDatatype string
}
