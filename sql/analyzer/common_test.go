package analyzer

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"testing"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/stretchr/testify/require"
	"gopkg.in/src-d/go-errors.v1"

	"github.com/liquidata-inc/go-mysql-server/sql"
	"github.com/liquidata-inc/go-mysql-server/sql/expression"
	"github.com/liquidata-inc/go-mysql-server/sql/plan"
)

func not(e sql.Expression) sql.Expression {
	return expression.NewNot(e)
}

func gt(left, right sql.Expression) sql.Expression {
	return expression.NewGreaterThan(left, right)
}

func gte(left, right sql.Expression) sql.Expression {
	return expression.NewGreaterThanOrEqual(left, right)
}

func lt(left, right sql.Expression) sql.Expression {
	return expression.NewLessThan(left, right)
}

func lte(left, right sql.Expression) sql.Expression {
	return expression.NewLessThanOrEqual(left, right)
}

func or(left, right sql.Expression) sql.Expression {
	return expression.NewOr(left, right)
}

func in(col sql.Expression, tuple sql.Expression) sql.Expression {
	return expression.NewInTuple(col, tuple)
}

func tuple(vals ...sql.Expression) sql.Expression {
	return expression.NewTuple(vals...)
}

func and(left, right sql.Expression) sql.Expression {
	return expression.NewAnd(left, right)
}

func col(idx int, table, col string) sql.Expression {
	return expression.NewGetFieldWithTable(idx, sql.Int64, table, col, false)
}

func eq(left, right sql.Expression) sql.Expression {
	return expression.NewEquals(left, right)
}

func lit(n int64) sql.Expression {
	return expression.NewLiteral(n, sql.Int64)
}

func gf(idx int, table, name string) *expression.GetField {
	return expression.NewGetFieldWithTable(idx, sql.Int64, table, name, false)
}

func uc(name string) *expression.UnresolvedColumn {
	return expression.NewUnresolvedColumn(name)
}

func uqc(table, name string) *expression.UnresolvedColumn {
	return expression.NewUnresolvedQualifiedColumn(table, name)
}

// Creates a new top-level scope from the node given
func newScope(n sql.Node) *Scope {
	return (*Scope)(nil).newScope(n)
}

var analyzeRules = [][]Rule{
	OnceBeforeDefault,
	DefaultRules,
	OnceAfterDefault,
}

func getRule(name string) Rule {
	for _, rules := range analyzeRules {
		rule := getRuleFrom(rules, name)
		if rule != nil {
			return *rule
		}
	}

	panic("missing rule")
}

func getRuleFrom(rules []Rule, name string) *Rule {
	for _, rule := range rules {
		if rule.Name == name {
			return &rule
		}
	}

	return nil
}

// Common test struct for analyzer transformation tests. Name and node are required, other fields are optional.
// The expected node is optional: if omitted, the tests asserts that input == output. The optional err field is the
// kind of error expected, if any.
type analyzerFnTestCase struct {
	name     string
	node     sql.Node
	scope    *Scope
	expected sql.Node
	err      *errors.Kind
}

func runTestCases(t *testing.T, ctx *sql.Context, testCases []analyzerFnTestCase, a *Analyzer, f Rule) {
	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			context := ctx
			if context == nil {
				context = sql.NewEmptyContext()
			}
			result, err := f.Apply(context, a, tt.node, tt.scope)
			if tt.err != nil {
				require.Error(t, err)
				require.True(t, tt.err.Is(err), fmt.Sprintf("Expected error of type %T but got %T", tt.err, err))
				return
			}
			require.NoError(t, err)

			expected := tt.expected
			if expected == nil {
				expected = tt.node
			}

			ensureSubquerySchema(expected)
			ensureSubquerySchema(result)
			assertNodesEqualWithDiff(t, expected, result)
		})
	}
}

// Since SubqueryAlias nodes' schemas are loaded on demand, this method loads the schema of any such nodes for use in
// test comparisons.
func ensureSubquerySchema(n sql.Node) {
	plan.Inspect(n, func(n sql.Node) bool {
		if _, ok := n.(*plan.SubqueryAlias); ok {
			_ = n.Schema()
		}
		return true
	})
}

// assertNodesEqualWithDiff asserts the two nodes given to be equal and prints any diff according to their DebugString
// methods.
func assertNodesEqualWithDiff(t *testing.T, expected, actual sql.Node) {
	if !assert.Equal(t, expected, actual) {
		expectedStr := sql.DebugString(expected)
		actualStr := sql.DebugString(actual)
		diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
			A:        difflib.SplitLines(expectedStr),
			B:        difflib.SplitLines(actualStr),
			FromFile: "expected",
			FromDate: "",
			ToFile:   "actual",
			ToDate:   "",
			Context:  1,
		})
		require.NoError(t, err)

		if len(diff) > 0 {
			fmt.Println(diff)
		}
	}
}
