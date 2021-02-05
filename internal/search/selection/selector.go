package selection

import "github.com/sourcegraph/sourcegraph/internal/search"

// Selector defines the Select() method, which allows conversion
// from one type into a parent type.
//
// Following is a work-in-progress sketch of the result type hierarchy.
// We don't yet have result types for some of these. Currently, only a
// parent type is selectable from a result type. For example, a repository
// can be selected from a file, but not the inverse.
//
//   repository
//   |
//   |- file
//   |  |
//   |  |- content
//   |  `- symbol
//   |
//   `- commit
//      |
//      |- message
//      |- diff-content
//      |- author
//      `- date-time
//
type Selector interface {
	// Select creates a new result of the passed Type.
	// If the passed Type is not supported by this selector,
	// it should return false as the second value.
	Select(Type) (search.Result, bool)
}
