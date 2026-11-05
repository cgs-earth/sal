package sparql

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlanNodeKeepsDuckDBDetailsInOrder(t *testing.T) {
	nodes, err := parsePlanNodes([]byte(`[{
		"name": "PROJECTION",
		"children": [{"name": "SEQ_SCAN", "children": [], "extra_info": {"Table": "triples", "Type": "Sequential Scan", "Estimated Cardinality": "10"}}],
		"extra_info": {"Expressions": ["a", "count_star()"], "Filters": "", "Estimated Cardinality": "6"}
	}]`))
	require.NoError(t, err)
	require.Equal(t, []PlanNode{{
		Name: "PROJECTION",
		Details: []PlanDetail{
			{Key: "Expressions", Values: []string{"a", "count_star()"}},
			{Key: "Estimated Cardinality", Values: []string{"6"}},
		},
		Children: []PlanNode{{
			Name: "SEQ_SCAN",
			Details: []PlanDetail{
				{Key: "Table", Values: []string{"triples"}},
				{Key: "Type", Values: []string{"Sequential Scan"}},
				{Key: "Estimated Cardinality", Values: []string{"10"}},
			},
			Children: []PlanNode{},
		}},
	}}, nodes, "details stay in the order DuckDB wrote them and an empty one is left out")
}

func TestPlanNodeWithoutDetailsOrChildrenRoundTripsAsEmptyLists(t *testing.T) {
	nodes, err := parsePlanNodes([]byte(`[{"name": "DUMMY_SCAN"}]`))
	require.NoError(t, err)
	encoded, err := json.Marshal(nodes)
	require.NoError(t, err)
	require.JSONEq(t, `[{"name":"DUMMY_SCAN","details":[],"children":[]}]`, string(encoded))
	var decoded []PlanNode
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, nodes, decoded, "what sal writes for a plan is what it reads back")
}

func TestPlanNodeRefusesADetailThatIsNotText(t *testing.T) {
	_, err := parsePlanNodes([]byte(`[{"name": "SEQ_SCAN", "extra_info": {"Rows": 10}}]`))
	require.ErrorContains(t, err, "extra_info Rows")
}

func TestExplainReportsAllThreePlansWithoutRunningTheStatement(t *testing.T) {
	runner := DuckDBRunner{}
	plan, err := runner.Explain(context.Background(), "SELECT a, count(*) AS n FROM (SELECT range AS a FROM range(10)) GROUP BY a ORDER BY a LIMIT 3;")
	require.NoError(t, err)

	require.Len(t, plan.Logical, 1)
	require.Len(t, plan.Optimized, 1)
	require.Len(t, plan.Physical, 1)
	require.Equal(t, "TOP_N", plan.Physical[0].Name, "the ORDER BY with a LIMIT becomes a top-N physically")
	require.NotEmpty(t, plan.Physical[0].Details)
	require.Contains(t, plan.Physical[0].Details, PlanDetail{Key: "Top", Values: []string{"3"}})
	var leaf PlanNode
	for leaf = plan.Physical[0]; len(leaf.Children) > 0; leaf = leaf.Children[0] {
	}
	require.Equal(t, "RANGE", leaf.Name, "the physical plan bottoms out in the table function the statement reads")

	// The setting the three plans were asked for with is a setting of the
	// connection, and must not stay set for the queries that follow.
	result, err := runner.RunSQL(context.Background(), "SELECT current_setting('explain_output') AS mode")
	require.NoError(t, err)
	require.Equal(t, [][]string{{"PHYSICAL_ONLY"}}, result.Rows)
}

func TestExplainReportsAStatementDuckDBCannotBind(t *testing.T) {
	_, err := DuckDBRunner{}.Explain(context.Background(), "SELECT * FROM no_such_table")
	require.ErrorContains(t, err, "no_such_table")
}
