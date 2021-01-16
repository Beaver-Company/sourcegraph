package query

import (
	"fmt"
	"regexp"

	"github.com/sourcegraph/sourcegraph/internal/search/query/syntax"
	"github.com/sourcegraph/sourcegraph/internal/search/query/types"
)

type ExpectedOperand struct {
	Msg string
}

func (e *ExpectedOperand) Error() string {
	return e.Msg
}

type UnsupportedError struct {
	Msg string
}

func (e *UnsupportedError) Error() string {
	return e.Msg
}

// LegacyParseError aliases to the parse error type for the legacy parser.
type LegacyParseError = syntax.ParseError

type SearchType int

const (
	SearchTypeRegex SearchType = iota
	SearchTypeLiteral
	SearchTypeStructural
)

// QueryInfo is an intermediate interface that satisfies the requirements of our
// internal functions, providing methods to access validated fields and values.
type QueryInfo interface {
	RegexpPatterns(field string) (values, negatedValues []string)
	StringValues(field string) (values, negatedValues []string)
	StringValue(field string) (value, negatedValue string)
	Values(field string) []*types.Value
	Fields() map[string][]*types.Value
	BoolValue(field string) bool
	IsCaseSensitive() bool
	ParseTree() syntax.ParseTree
}

// A query containing and/or expressions.
type AndOrQuery struct {
	Query []Node
}

// AndOrQuery satisfies the interface for QueryInfo.
func (q AndOrQuery) RegexpPatterns(field string) (values, negatedValues []string) {
	VisitField(q.Query, field, func(visitedValue string, negated bool, _ Annotation) {
		if negated {
			negatedValues = append(negatedValues, visitedValue)
		} else {
			values = append(values, visitedValue)
		}
	})
	return values, negatedValues
}

func (q AndOrQuery) StringValues(field string) (values, negatedValues []string) {
	VisitField(q.Query, field, func(visitedValue string, negated bool, _ Annotation) {
		if negated {
			negatedValues = append(negatedValues, visitedValue)
		} else {
			values = append(values, visitedValue)
		}
	})
	return values, negatedValues
}

func (q AndOrQuery) StringValue(field string) (value, negatedValue string) {
	VisitField(q.Query, field, func(visitedValue string, negated bool, _ Annotation) {
		if negated {
			negatedValue = visitedValue
		} else {
			value = visitedValue
		}
	})
	return value, negatedValue
}

func (q AndOrQuery) Values(field string) []*types.Value {
	var values []*types.Value
	if field == "" {
		VisitPattern(q.Query, func(value string, _ bool, annotation Annotation) {
			values = append(values, q.valueToTypedValue(field, value, annotation.Labels)...)
		})
	} else {
		VisitField(q.Query, field, func(value string, _ bool, _ Annotation) {
			values = append(values, q.valueToTypedValue(field, value, None)...)
		})
	}
	return values
}

func (q AndOrQuery) Fields() map[string][]*types.Value {
	fields := make(map[string][]*types.Value)
	VisitPattern(q.Query, func(value string, _ bool, _ Annotation) {
		fields[""] = q.Values("")
	})
	VisitParameter(q.Query, func(field, _ string, _ bool, _ Annotation) {
		fields[field] = q.Values(field)
	})
	return fields
}

// ParseTree returns a flat, mock-like parse tree of an and/or query. The parse
// tree values are currently only significant in alerts. Whether it is empty or
// not is significant for surfacing suggestions.
func (q AndOrQuery) ParseTree() syntax.ParseTree {
	var tree syntax.ParseTree
	VisitPattern(q.Query, func(value string, negated bool, _ Annotation) {
		expr := &syntax.Expr{
			Field: "",
			Value: value,
			Not:   negated,
		}
		tree = append(tree, expr)
	})
	VisitParameter(q.Query, func(field, value string, negated bool, _ Annotation) {
		expr := &syntax.Expr{
			Field: field,
			Value: value,
			Not:   negated,
		}
		tree = append(tree, expr)
	})
	return tree
}

func (q AndOrQuery) BoolValue(field string) bool {
	result := false
	VisitField(q.Query, field, func(value string, _ bool, _ Annotation) {
		result, _ = parseBool(value) // err was checked during parsing and validation.
	})
	return result
}

func (q AndOrQuery) IsCaseSensitive() bool {
	return q.BoolValue("case")
}

func parseRegexpOrPanic(field, value string) *regexp.Regexp {
	r, err := regexp.Compile(value)
	if err != nil {
		panic(fmt.Sprintf("Value %s for field %s invalid regex: %s", field, value, err.Error()))
	}
	return r
}

// valueToTypedValue converts syntax to internal values after valudation.
func (q AndOrQuery) valueToTypedValue(field, value string, label labels) []*types.Value {
	switch field {
	case
		FieldDefault:
		if label.isSet(Literal) {
			return []*types.Value{{String: &value}}
		}
		if label.isSet(Regexp) {
			regexp, err := regexp.Compile(value)
			if err != nil {
				panic(fmt.Sprintf("Invariant broken: value must have been checked to be valid regexp. Error: %s", err))
			}
			return []*types.Value{{Regexp: regexp}}
		}
		// All patterns should have a label after parsing, but if not, treat the pattern as a string literal.
		return []*types.Value{{String: &value}}

	case
		FieldCase:
		b, _ := parseBool(value)
		return []*types.Value{{Bool: &b}}

	case
		FieldRepo, "r":
		return []*types.Value{{Regexp: parseRegexpOrPanic(field, value)}}

	case
		FieldRepoGroup, "g":
		return []*types.Value{{String: &value}}

	case
		FieldFile, "f":
		return []*types.Value{{Regexp: parseRegexpOrPanic(field, value)}}

	case
		FieldFork,
		FieldArchived,
		FieldLang, "l", "language",
		FieldType,
		FieldPatternType,
		FieldContent:
		return []*types.Value{{String: &value}}

	case FieldRepoHasFile:
		return []*types.Value{{Regexp: parseRegexpOrPanic(field, value)}}

	case
		FieldRepoHasCommitAfter,
		FieldBefore, "until",
		FieldAfter, "since":
		return []*types.Value{{String: &value}}

	case
		FieldAuthor,
		FieldCommitter,
		FieldMessage, "m", "msg":
		return []*types.Value{{Regexp: parseRegexpOrPanic(field, value)}}

	case
		FieldIndex,
		FieldCount,
		FieldMax,
		FieldTimeout,
		FieldCombyRule:
		return []*types.Value{{String: &value}}
	}
	return []*types.Value{{String: &value}}
}
