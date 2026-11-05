package sparql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cgs-earth/sal/pkg/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// QueryPlan is how DuckDB would answer a statement, at the three stages its
// EXPLAIN reports: the logical plan the binder built from the statement, that
// plan after the optimizer, and the physical operators that would run. Each
// is the roots of a tree, one per statement explained.
type QueryPlan struct {
	Logical   []PlanNode `json:"logical"`
	Optimized []PlanNode `json:"optimized"`
	Physical  []PlanNode `json:"physical"`
}

// PlanNode is one operator of a plan with the operators it reads from.
type PlanNode struct {
	Name string `json:"name"`
	// Details are what DuckDB says about the operator (the table a scan
	// reads, the expressions a projection computes, its estimated
	// cardinality), in the order DuckDB wrote them.
	Details  []PlanDetail `json:"details"`
	Children []PlanNode   `json:"children"`
}

// PlanDetail is one entry of an operator's extra_info. A value DuckDB writes
// on several lines, the expressions of a projection say, arrives as several
// values.
type PlanDetail struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

// duckdbPlanNode is a node as DuckDB's JSON EXPLAIN writes it, read apart from
// PlanNode so that a PlanNode's own JSON stays symmetric.
type duckdbPlanNode struct {
	Name     string           `json:"name"`
	Children []duckdbPlanNode `json:"children"`
	Info     json.RawMessage  `json:"extra_info"`
}

// parsePlanNodes reads the roots of one plan as DuckDB wrote them.
func parsePlanNodes(data []byte) ([]PlanNode, error) {
	var raw []duckdbPlanNode
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	nodes := make([]PlanNode, 0, len(raw))
	for _, node := range raw {
		converted, err := node.planNode()
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, converted)
	}
	return nodes, nil
}

// planNode converts the node, keeping the extra_info entries in the order
// DuckDB wrote them, which a map would lose.
func (n duckdbPlanNode) planNode() (PlanNode, error) {
	details, err := planDetails(n.Info)
	if err != nil {
		return PlanNode{}, fmt.Errorf("read the extra_info of %s: %w", n.Name, err)
	}
	children := make([]PlanNode, 0, len(n.Children))
	for _, child := range n.Children {
		converted, err := child.planNode()
		if err != nil {
			return PlanNode{}, err
		}
		children = append(children, converted)
	}
	return PlanNode{Name: n.Name, Details: details, Children: children}, nil
}

// planDetails reads an extra_info object in document order. A value is a
// string or an array of strings; an entry with nothing to say (DuckDB writes
// an empty string for the expressions of an operator that has none) is left
// out.
func planDetails(info json.RawMessage) ([]PlanDetail, error) {
	details := []PlanDetail{}
	if len(info) == 0 || string(info) == "null" {
		return details, nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(info)))
	if token, err := decoder.Token(); err != nil {
		return nil, err
	} else if token != json.Delim('{') {
		return nil, fmt.Errorf("extra_info is not an object")
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("extra_info key %v is not a string", token)
		}
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		values, err := planDetailValues(value)
		if err != nil {
			return nil, fmt.Errorf("extra_info %s: %w", key, err)
		}
		if len(values) > 0 {
			details = append(details, PlanDetail{Key: key, Values: values})
		}
	}
	return details, nil
}

func planDetailValues(value any) ([]string, error) {
	switch v := value.(type) {
	case string:
		if v == "" {
			return nil, nil
		}
		return []string{v}, nil
	case []any:
		values := make([]string, 0, len(v))
		for _, item := range v {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("value %v is not a string", item)
			}
			values = append(values, text)
		}
		return values, nil
	default:
		return nil, fmt.Errorf("value %v is neither a string nor an array", value)
	}
}

// Explain reports how DuckDB would run a statement against the triples
// table, without running it: the statement is bound and optimized, which
// reads the table's Iceberg metadata but none of its data.
func (r DuckDBRunner) Explain(ctx context.Context, statement string) (_ QueryPlan, err error) {
	ctx, span := telemetry.Start(ctx, "duckdb.explain", attribute.String("db.system.name", "duckdb"), attribute.String("db.query.text", queryTextAttribute(statement)))
	defer func() { telemetry.End(span, err) }()
	withSpatial := needsSpatial(statement)
	plan, err := r.explain(ctx, statement, withSpatial)
	// Binding can need spatial in a way needsSpatial does not recognize, the
	// same as running does.
	if err != nil && !withSpatial && missingSpatialExtension(err) {
		plan, err = r.explain(ctx, statement, true)
	}
	return plan, err
}

func (r DuckDBRunner) explain(ctx context.Context, statement string, withSpatial bool) (QueryPlan, error) {
	db, err := instance.prepare(ctx, r.TablePath, r.Imports, withSpatial)
	if err != nil {
		return QueryPlan{}, err
	}
	// explain_output is a setting of the connection rather than of the
	// database, so the three plans are asked for, and the default put back,
	// on the one connection the EXPLAIN runs on.
	conn, err := db.Conn(ctx)
	if err != nil {
		return QueryPlan{}, fmt.Errorf("duckdb explain failed: %w", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			slog.Warn("failed to release the duckdb connection", "error", err)
		}
	}()
	if _, err := conn.ExecContext(ctx, "SET explain_output = 'all'"); err != nil {
		return QueryPlan{}, fmt.Errorf("duckdb explain failed: %w", err)
	}
	defer func() {
		if _, err := conn.ExecContext(ctx, "RESET explain_output"); err != nil {
			slog.Warn("failed to reset the duckdb explain_output setting", "error", err)
		}
	}()
	rows, err := conn.QueryContext(ctx, "EXPLAIN (FORMAT json) "+strings.TrimRight(strings.TrimSpace(statement), ";"))
	if err != nil {
		return QueryPlan{}, fmt.Errorf("duckdb explain failed: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			slog.Warn("failed to close the duckdb result", "error", err)
		}
	}()
	return readPlan(rows)
}

// readPlan reads the explain_key/explain_value rows of an EXPLAIN into the
// three plans they carry.
func readPlan(rows *sql.Rows) (QueryPlan, error) {
	var plan QueryPlan
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return QueryPlan{}, fmt.Errorf("read the duckdb plan: %w", err)
		}
		nodes, err := parsePlanNodes([]byte(value))
		if err != nil {
			return QueryPlan{}, fmt.Errorf("read the duckdb %s: %w", key, err)
		}
		switch key {
		case "logical_plan":
			plan.Logical = nodes
		case "logical_opt":
			plan.Optimized = nodes
		case "physical_plan":
			plan.Physical = nodes
		default:
			slog.Warn("ignoring a duckdb plan sal does not know", "key", key)
		}
	}
	if err := rows.Err(); err != nil {
		return QueryPlan{}, fmt.Errorf("duckdb explain failed: %w", err)
	}
	if plan.Logical == nil || plan.Optimized == nil || plan.Physical == nil {
		return QueryPlan{}, fmt.Errorf("duckdb explain failed: the logical, optimized, and physical plans were not all reported")
	}
	return plan, nil
}
