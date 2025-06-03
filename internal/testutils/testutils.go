// Package testutils provides utility functions for testing Argo functionality.
package testutils

import (
	"fmt"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/parser"
)

const argoDirectivesString = `
enum ArgoCodecType { String Int Float Boolean BYTES FIXED DESC }
directive @ArgoCodec(codec: ArgoCodecType!, fixedLength: Int) on SCALAR | ENUM
directive @ArgoDeduplicate(deduplicate: Boolean! = true) on SCALAR | ENUM
`

// LoadSchema parses a GraphQL schema string, adds Argo directives, and validates it.
func LoadSchema(schemaString string) (*ast.Schema, error) {
	userSchemaSource := &ast.Source{Name: "userSchema", Input: schemaString}
	argoDirectivesSource := &ast.Source{Name: "argoDirectives", Input: argoDirectivesString}

	// Use gqlparser.LoadSchema, which also adds the GraphQL prelude.
	schema, err := gqlparser.LoadSchema(argoDirectivesSource, userSchemaSource)
	if err != nil {
		return nil, err // Return the original error from gqlparser
	}
	return schema, nil
}

// LoadQuery parses a GraphQL query string using gqlparser.
func LoadQuery(queryString string) (*ast.QueryDocument, *gqlerror.Error) {
	queryDoc, err := parser.ParseQuery(&ast.Source{Name: "testQuery", Input: queryString})
	if err != nil {
		if gqlErr, ok := err.(*gqlerror.Error); ok {
			return nil, gqlErr
		}
		return nil, &gqlerror.Error{Message: fmt.Sprintf("query parsing failed: %s", err.Error()), Extensions: map[string]interface{}{"code": "QUERY_PARSE_ERROR"}}
	}
	return queryDoc, nil
}

// JoinPath concatenates path elements with a forward slash.
// This is a basic utility for constructing test paths and is not OS-aware.
func JoinPath(elements ...string) string {
	// Basic implementation, can be improved if needed.
	if len(elements) == 0 {
		return ""
	}
	path := elements[0]
	for i := 1; i < len(elements); i++ {
		path += "/" + elements[i] // Simple concatenation, might need to be OS-aware
	}
	return path
}
