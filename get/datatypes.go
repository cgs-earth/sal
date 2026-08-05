package get

import (
	"fmt"

	salsparql "github.com/cgs-earth/sal/query/sparql"
)

type datatypesCmd struct{}

func (cmd *datatypesCmd) Run() error {
	result, err := runLookup(salsparql.DatatypesSQL)
	if err != nil {
		return err
	}
	if len(result.Rows) == 0 {
		fmt.Println("no RDF datatypes found; the data product declares no rdfs:Datatype resources")
		return nil
	}
	header, rows := dropEmptyColumns(result.Header, result.Rows)
	fmt.Print(formatTable(header, rows))
	return nil
}

// dropEmptyColumns removes the columns that are empty for every row, so that
// the optional rdfs:label and rdfs:comment annotations are only printed by a
// data product that actually states them.
func dropEmptyColumns(header []string, rows [][]string) ([]string, [][]string) {
	kept := make([]int, 0, len(header))
	for column := range header {
		for _, row := range rows {
			if column < len(row) && row[column] != "" {
				kept = append(kept, column)
				break
			}
		}
	}
	if len(kept) == len(header) {
		return header, rows
	}

	keptHeader := make([]string, 0, len(kept))
	for _, column := range kept {
		keptHeader = append(keptHeader, header[column])
	}
	keptRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		keptRow := make([]string, 0, len(kept))
		for _, column := range kept {
			if column < len(row) {
				keptRow = append(keptRow, row[column])
			}
		}
		keptRows = append(keptRows, keptRow)
	}
	return keptHeader, keptRows
}
