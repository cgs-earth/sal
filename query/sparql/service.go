package sparql

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	rdflibgo "github.com/tggo/goRDFlib"
	rdflibsparql "github.com/tggo/goRDFlib/sparql"
)

type serviceBlock struct {
	Endpoint string
	Body     string
	Vars     []string
	Query    string
}

type federatedPlan struct {
	LocalSQL   string
	LocalVars  []string
	Projection []string
	Services   []serviceBlock
	Distinct   bool
	Limit      int
}

// federatedPlanFor splits SERVICE blocks out of a SPARQL SELECT and compiles the
// local portion into a DuckDB query whose rows can be joined with remote rows.
func federatedPlanFor(input string, layout ObjectLayout) (*federatedPlan, error) {
	stripped, services, err := extractServiceBlocks(input)
	if err != nil {
		return nil, err
	}
	if len(services) == 0 {
		return nil, nil
	}

	parsed, err := rdflibsparql.Parse(stripped)
	if err != nil {
		return nil, fmt.Errorf("parse SPARQL query without SERVICE blocks: %w", err)
	}
	localParsed := *parsed
	localParsed.Variables = nil
	localParsed.Distinct = false
	localParsed.Limit = -1
	localSQL, localVars, err := toSQL(&localParsed, layout, nil, true, false)
	if err != nil {
		return nil, err
	}

	prefixes := prefixDeclarations(input)
	for i := range services {
		vars, err := serviceVariables(prefixes, services[i].Body)
		if err != nil {
			return nil, fmt.Errorf("SERVICE <%s>: %w", services[i].Endpoint, err)
		}
		services[i].Vars = vars
		serviceLimit := -1
		if parsed.Limit >= 0 && !hasSharedVariable(localVars, vars) {
			serviceLimit = parsed.Limit
		}
		services[i].Query = serviceQuery(prefixes, vars, services[i].Body, serviceLimit)
	}

	projection := parsed.Variables
	if len(projection) == 0 {
		projection = append([]string{}, localVars...)
		for _, service := range services {
			for _, name := range service.Vars {
				if !slices.Contains(projection, name) {
					projection = append(projection, name)
				}
			}
		}
	}
	if len(projection) == 0 {
		return nil, fmt.Errorf("SPARQL SELECT must project at least one variable")
	}
	for _, name := range projection {
		if !slices.Contains(localVars, name) && !serviceVarBound(services, name) {
			return nil, fmt.Errorf("projected variable ?%s is not bound by a local pattern or SERVICE block", name)
		}
	}
	if parsed.Limit >= 0 && !servicesShareLocalVariable(localVars, services) {
		localLimit := parsed.Limit
		if !projectionIncludesLocalVariable(projection, localVars) {
			localLimit = 1
		}
		localSQL = appendSQLLimit(localSQL, localLimit)
	}

	return &federatedPlan{
		LocalSQL:   localSQL,
		LocalVars:  localVars,
		Projection: projection,
		Services:   services,
		Distinct:   parsed.Distinct,
		Limit:      parsed.Limit,
	}, nil
}

func serviceVarBound(services []serviceBlock, name string) bool {
	for _, service := range services {
		if slices.Contains(service.Vars, name) {
			return true
		}
	}
	return false
}

func serviceVariables(prefixes string, body string) ([]string, error) {
	parsed, err := rdflibsparql.Parse(prefixes + "\nSELECT * WHERE {\n" + body + "\n}")
	if err != nil {
		return nil, fmt.Errorf("parse SERVICE graph pattern: %w", err)
	}
	triples, filters, err := basicGraphPatternParts(parsed.Where)
	if err != nil {
		return nil, err
	}
	if len(triples) == 0 {
		return nil, fmt.Errorf("SERVICE block must include at least one triple pattern")
	}
	bindings := make(map[string]bool)
	var vars []string
	for _, triple := range triples {
		for _, term := range []string{triple.Subject, triple.Predicate, triple.Object} {
			name := variableName(term)
			if name != "" && !bindings[name] {
				bindings[name] = true
				vars = append(vars, name)
			}
		}
	}
	for _, filter := range filters {
		for _, name := range exprVars(filter) {
			if !bindings[name] {
				return nil, fmt.Errorf("FILTER variable ?%s is not bound by a supported SERVICE triple pattern", name)
			}
		}
	}
	if len(vars) == 0 {
		return nil, fmt.Errorf("SERVICE block must bind at least one variable")
	}
	return vars, nil
}

func exprVars(expr rdflibsparql.Expr) []string {
	switch e := expr.(type) {
	case *rdflibsparql.VarExpr:
		return []string{e.Name}
	case *rdflibsparql.BinaryExpr:
		return append(exprVars(e.Left), exprVars(e.Right)...)
	case *rdflibsparql.UnaryExpr:
		return exprVars(e.Arg)
	case *rdflibsparql.FuncExpr:
		var vars []string
		for _, arg := range e.Args {
			vars = append(vars, exprVars(arg)...)
		}
		return vars
	default:
		return nil
	}
}

func hasSharedVariable(left []string, right []string) bool {
	for _, name := range left {
		if slices.Contains(right, name) {
			return true
		}
	}
	return false
}

func servicesShareLocalVariable(localVars []string, services []serviceBlock) bool {
	for _, service := range services {
		if hasSharedVariable(localVars, service.Vars) {
			return true
		}
	}
	return false
}

func projectionIncludesLocalVariable(projection []string, localVars []string) bool {
	for _, name := range projection {
		if slices.Contains(localVars, name) {
			return true
		}
	}
	return false
}

func appendSQLLimit(sql string, limit int) string {
	if limit < 0 || hasLimit(sql) {
		return sql
	}
	return strings.TrimRight(strings.TrimSpace(sql), ";") + "\nLIMIT " + strconv.Itoa(limit)
}

func serviceQuery(prefixes string, vars []string, body string, limit int) string {
	projected := make([]string, 0, len(vars))
	for _, name := range vars {
		projected = append(projected, "?"+name)
	}
	query := strings.TrimSpace(prefixes + "\nSELECT DISTINCT " + strings.Join(projected, " ") + " WHERE {\n" + body + "\n}")
	if limit >= 0 {
		query += "\nLIMIT " + strconv.Itoa(limit)
	}
	return query
}

func prefixDeclarations(input string) string {
	var lines []string
	for _, line := range strings.Split(input, "\n") {
		trimmed := strings.TrimSpace(line)
		fields := strings.Fields(trimmed)
		if len(fields) > 0 && (strings.EqualFold(fields[0], "PREFIX") || strings.EqualFold(fields[0], "BASE")) {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// fetchServiceRows calls a SPARQL endpoint and returns SELECT result rows in
// the requested SERVICE variable order.
func fetchServiceRows(ctx context.Context, client *http.Client, service serviceBlock) ([][]string, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	form := url.Values{"query": {service.Query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, service.Endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create SERVICE request: %w", err)
	}
	req.Header.Set("Accept", sparqlResultsJSON)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call SERVICE endpoint: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("SERVICE endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	result, err := rdflibsparql.ParseSRJ(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse SERVICE SPARQL JSON result: %w", err)
	}
	if result.Type != "SELECT" {
		return nil, fmt.Errorf("SERVICE endpoint returned %s result, expected SELECT", result.Type)
	}

	rows := make([][]string, 0, len(result.Bindings))
	for _, binding := range result.Bindings {
		row := make([]string, len(service.Vars))
		for i, name := range service.Vars {
			if term := binding[name]; term != nil {
				row[i] = termValue(term)
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func termValue(term rdflibgo.Term) string {
	return term.String()
}

func federatedSQL(plan *federatedPlan, serviceRows [][][]string, limit int) string {
	var ctes []string
	ctes = append(ctes, "local_result AS (\n"+plan.LocalSQL+"\n)")
	for i, service := range plan.Services {
		ctes = append(ctes, serviceValuesCTE(i, service.Vars, serviceRows[i]))
	}

	boundBy := make(map[string]string)
	for _, name := range plan.LocalVars {
		boundBy[name] = "l"
	}

	from := "local_result AS l"
	for i, service := range plan.Services {
		alias := fmt.Sprintf("s%d", i)
		var joins []string
		for _, name := range service.Vars {
			if previous := boundBy[name]; previous != "" {
				joins = append(joins, previous+"."+quoteIdent(name)+" = "+alias+"."+quoteIdent(name))
			}
		}
		if len(joins) == 0 {
			from += "\nCROSS JOIN service_" + strconv.Itoa(i) + " AS " + alias
		} else {
			from += "\nINNER JOIN service_" + strconv.Itoa(i) + " AS " + alias + " ON " + strings.Join(joins, " AND ")
		}
		for _, name := range service.Vars {
			if boundBy[name] == "" {
				boundBy[name] = alias
			}
		}
	}

	selects := make([]string, 0, len(plan.Projection))
	for _, name := range plan.Projection {
		selects = append(selects, boundBy[name]+"."+quoteIdent(name)+" AS "+quoteIdent(name))
	}
	prefix := "SELECT "
	if plan.Distinct {
		prefix = "SELECT DISTINCT "
	}
	sql := "WITH " + strings.Join(ctes, ",\n") + "\n" + prefix + strings.Join(selects, ", ") + "\nFROM " + from
	if plan.Limit >= 0 {
		sql += "\nLIMIT " + strconv.Itoa(plan.Limit)
	} else if limit > 0 {
		sql += "\nLIMIT " + strconv.Itoa(limit)
	}
	return sql
}

func serviceValuesCTE(index int, vars []string, rows [][]string) string {
	name := "service_" + strconv.Itoa(index)
	if len(rows) == 0 {
		selects := make([]string, 0, len(vars))
		for _, variable := range vars {
			selects = append(selects, "NULL::VARCHAR AS "+quoteIdent(variable))
		}
		return name + " AS (SELECT " + strings.Join(selects, ", ") + " WHERE false)"
	}
	values := make([]string, 0, len(rows))
	for _, row := range rows {
		items := make([]string, 0, len(vars))
		for i := range vars {
			value := ""
			if i < len(row) {
				value = row[i]
			}
			items = append(items, sqlString(value))
		}
		values = append(values, "("+strings.Join(items, ", ")+")")
	}
	columns := make([]string, 0, len(vars))
	for _, variable := range vars {
		columns = append(columns, quoteIdent(variable))
	}
	return name + "(" + strings.Join(columns, ", ") + ") AS (VALUES " + strings.Join(values, ", ") + ")"
}

func extractServiceBlocks(input string) (string, []serviceBlock, error) {
	output := []byte(input)
	var services []serviceBlock
	for i := 0; i < len(input); {
		if shouldSkip(input[i]) {
			i = skipSPARQLToken(input, i)
			continue
		}
		if !keywordAt(input, i, "SERVICE") {
			i++
			continue
		}
		start := i
		i += len("SERVICE")
		i = skipWhitespace(input, i)
		if keywordAt(input, i, "SILENT") {
			return "", nil, fmt.Errorf("SERVICE SILENT is not supported yet")
		}
		if i >= len(input) || input[i] != '<' {
			return "", nil, fmt.Errorf("SERVICE endpoint must be a constant IRI")
		}
		endpointEnd := strings.IndexByte(input[i:], '>')
		if endpointEnd < 0 {
			return "", nil, fmt.Errorf("unterminated SERVICE endpoint IRI")
		}
		endpoint := input[i+1 : i+endpointEnd]
		i += endpointEnd + 1
		i = skipWhitespace(input, i)
		if i >= len(input) || input[i] != '{' {
			return "", nil, fmt.Errorf("SERVICE endpoint must be followed by a graph pattern")
		}
		bodyStart := i + 1
		blockEnd, err := matchingBrace(input, i)
		if err != nil {
			return "", nil, err
		}
		body := input[bodyStart:blockEnd]
		i = blockEnd + 1
		after := skipWhitespace(input, i)
		if after < len(input) && input[after] == '.' {
			i = after + 1
		}
		blankPreservingNewlines(output, start, i)
		services = append(services, serviceBlock{Endpoint: endpoint, Body: body})
	}
	return string(output), services, nil
}

func shouldSkip(ch byte) bool {
	return ch == '"' || ch == '\'' || ch == '<' || ch == '#'
}

func skipSPARQLToken(input string, pos int) int {
	switch input[pos] {
	case '#':
		for pos < len(input) && input[pos] != '\n' {
			pos++
		}
		return pos
	case '<':
		for pos < len(input) && input[pos] != '>' {
			pos++
		}
		if pos < len(input) {
			pos++
		}
		return pos
	case '"', '\'':
		quote := input[pos]
		pos++
		escaped := false
		for pos < len(input) {
			if escaped {
				escaped = false
				pos++
				continue
			}
			if input[pos] == '\\' {
				escaped = true
				pos++
				continue
			}
			if input[pos] == quote {
				pos++
				return pos
			}
			pos++
		}
	}
	return pos + 1
}

func matchingBrace(input string, open int) (int, error) {
	depth := 0
	for i := open; i < len(input); {
		if shouldSkip(input[i]) {
			i = skipSPARQLToken(input, i)
			continue
		}
		switch input[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
		i++
	}
	return 0, fmt.Errorf("unterminated SERVICE graph pattern")
}

func blankPreservingNewlines(output []byte, start int, end int) {
	for i := start; i < end; i++ {
		if output[i] != '\n' && output[i] != '\r' {
			output[i] = ' '
		}
	}
}

func keywordAt(input string, pos int, keyword string) bool {
	if pos+len(keyword) > len(input) || !strings.EqualFold(input[pos:pos+len(keyword)], keyword) {
		return false
	}
	beforeOK := pos == 0 || !isKeywordRune(rune(input[pos-1]))
	after := pos + len(keyword)
	afterOK := after == len(input) || !isKeywordRune(rune(input[after]))
	return beforeOK && afterOK
}

func isKeywordRune(ch rune) bool {
	return unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_'
}

func skipWhitespace(input string, pos int) int {
	for pos < len(input) && unicode.IsSpace(rune(input[pos])) {
		pos++
	}
	return pos
}

func csvFromFederatedSQL(ctx context.Context, tablePath string, sql string) ([]byte, error) {
	tablePath = strings.ReplaceAll(tablePath, "'", "''")
	statement := fmt.Sprintf(`
INSTALL iceberg;
LOAD iceberg;
INSTALL spatial;
LOAD spatial;
CREATE OR REPLACE VIEW triples AS
SELECT *
FROM iceberg_scan('%s', allow_moved_paths = true);
COPY (%s) TO STDOUT (HEADER, DELIMITER ',');
`, tablePath, sql)

	cmd := duckDBCommand(ctx)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdin = strings.NewReader(statement)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("duckdb query failed: %s", strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("duckdb query failed: %w", err)
	}
	return stdout.Bytes(), nil
}
