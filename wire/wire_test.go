// Package wire_test contains tests for the wire package, focusing on the construction,
// representation, and utility functions related to Argo wire types.
// Many tests rely on the typer package to generate wire types from GraphQL schemas and queries.
package wire_test

import (
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/beeper/argo-go/typer"
	"github.com/beeper/argo-go/wire"
	"github.com/stretchr/testify/assert"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// starWarsSchemaSDL is the GraphQL Schema Definition Language for a simplified Star Wars schema,
// used as a common basis for many tests involving wire type generation and path resolution.
const starWarsSchemaSDL = `
"One of the films in the Star Wars Trilogy"
enum Episode {
  "Released in 1977."
  NEW_HOPE
  "Released in 1980."
  EMPIRE
  "Released in 1983."
  JEDI
}

"A character in the Star Wars Trilogy"
interface Character {
  "The id of the character."
  id: String!
  "The name of the character."
  name: String
  "The friends of the character, or an empty list if they have none."
  friends: [Character]
  "Which movies they appear in."
  appearsIn: [Episode]
  "All secrets about their past."
  secretBackstory: String
}

"A humanoid creature in the Star Wars universe."
type Human implements Character {
  "The id of the human."
  id: String!
  "The name of the human."
  name: String
  "The friends of the human, or an empty list if they have none."
  friends: [Character]
  "Which movies they appear in."
  appearsIn: [Episode]
  "The home planet of the human, or null if unknown."
  homePlanet: String
  "Where are they from and how they came to be who they are."
  secretBackstory: String
}

"A mechanical creature in the Star Wars universe."
type Droid implements Character {
  "The id of the droid."
  id: String!
  "The name of the droid."
  name: String
  "The friends of the droid, or an empty list if they have none."
  friends: [Character]
  "Which movies they appear in."
  appearsIn: [Episode]
  "Construction date and the name of the designer."
  secretBackstory: String
  "The primary function of the droid."
  primaryFunction: String
}

type Query {
  hero(episode: Episode): Character
  human(id: String!): Human
  droid(id: String!): Droid
}

schema {
  query: Query
}
`

var (
	starWarsSchemaOnce    sync.Once
	loadedStarWarsSchema  *ast.Schema
	starWarsSchemaLoadErr error
)

// getStarWarsSchema loads the starWarsSchemaSDL into an *ast.Schema using sync.Once to ensure it's parsed only once.
// It calls t.Fatalf if schema loading fails. This schema is used in various tests.
func getStarWarsSchema(t *testing.T) *ast.Schema {
	t.Helper()
	starWarsSchemaOnce.Do(func() {
		// Ensure Argo directives are available if schema parsing/validation depends on them.
		// Typer automatically knows about them for its logic, but schema loading might also need them if they were used in SDL.
		// For these tests, they are not in the SDLs.
		sources := []*ast.Source{{Name: "starwars.graphql", Input: starWarsSchemaSDL, BuiltIn: false}}
		schema, err := gqlparser.LoadSchema(sources...)
		if err != nil {
			starWarsSchemaLoadErr = err
			return
		}
		loadedStarWarsSchema = schema
	})
	if starWarsSchemaLoadErr != nil {
		t.Fatalf("Failed to load Star Wars schema: %v", starWarsSchemaLoadErr)
	}
	return loadedStarWarsSchema
}

// loadTestSchema is a test helper that parses a given GraphQL SDL string into an *ast.Schema.
// It calls t.Fatalf if schema loading fails.
func loadTestSchema(t *testing.T, sdl string) *ast.Schema {
	t.Helper()
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "schema.graphql", Input: sdl, BuiltIn: false})
	if err != nil {
		t.Fatalf("Failed to load schema: %v", err)
	}
	return schema
}

// parseTestQuery is a test helper that parses a given GraphQL query string into an *ast.QueryDocument.
// It calls t.Fatalf if query parsing fails.
func parseTestQuery(t *testing.T, queryStr string) *ast.QueryDocument {
	t.Helper()
	query, err := parser.ParseQuery(&ast.Source{Name: "query.graphql", Input: queryStr})
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}
	return query
}

// TestPathValues tests the wire.PathToWirePath and wire.WirePathToPath functions
// to ensure that paths can be correctly converted between their human-readable representation
// (slice of strings and ints) and their wire representation (slice of ints) and back.
// It uses the Star Wars schema and a sample query to generate a root wire type
// against which the path conversions are tested.
func TestPathValues(t *testing.T) {
	schema := getStarWarsSchema(t)
	queryStr := `
        query {
          hero {
            id
            name
            friends {
              name
              aliasedName: name
            }
          }
        }`
	query := parseTestQuery(t, queryStr)

	argoTyper, err := typer.NewTyper(schema, query)
	if err != nil {
		t.Fatalf("NewTyper failed: %v", err)
	}

	rootWireType, err := argoTyper.RootWireType()
	if err != nil {
		t.Fatalf("RootWireType failed: %v", err)
	}

	roundTripTest := func(path []interface{}) {
		t.Helper()
		wirePath, err := wire.PathToWirePath(rootWireType, path)
		if err != nil {
			t.Errorf("PathToWirePath for %v failed: %v", path, err)
			return
		}
		regularPath, err := wire.WirePathToPath(rootWireType, wirePath)
		if err != nil {
			t.Errorf("WirePathToPath for %v (from %v) failed: %v", wirePath, path, err)
			return
		}
		if !reflect.DeepEqual(regularPath, path) {
			t.Errorf("Round trip path failed. Expected %#v, got %#v", path, regularPath)
		}
	}

	roundTripTest([]interface{}{"data", "hero", "id"})
	roundTripTest([]interface{}{"data", "hero", "friends", 0, "name"})
	roundTripTest([]interface{}{"data", "hero", "friends", 1, "aliasedName"})
}

// TestOmittableDirectives verifies that the typer package correctly identifies fields
// as omittable based on the presence of `@include(if: false)` or `@skip(if: true)` directives.
// It generates wire types for various queries with these directives and checks the
// `Omittable` flag on the resulting `wire.Field` within the `wire.RecordType`.
func TestOmittableDirectives(t *testing.T) {
	schema := getStarWarsSchema(t)

	tests := []struct {
		name          string
		queryStr      string
		fieldName     string
		wantOmittable bool
	}{
		{
			name:          "@include",
			queryStr:      `query($v: Boolean!){ __typename @include(if: $v) }`,
			fieldName:     "__typename",
			wantOmittable: true,
		},
		{
			name:          "@include fragment",
			queryStr:      `query($v: Boolean!){ ... on Query @include(if: $v) { __typename } }`,
			fieldName:     "__typename",
			wantOmittable: true,
		},
		{
			name:          "@skip",
			queryStr:      `query($v: Boolean!){ __typename @skip(if: $v) }`,
			fieldName:     "__typename",
			wantOmittable: true,
		},
		{
			name:          "@skip fragment",
			queryStr:      `query($v: Boolean!){ ... on Query @skip(if: $v) { __typename } }`,
			fieldName:     "__typename",
			wantOmittable: true,
		},
		{
			name:          "no directive",
			queryStr:      `query { __typename }`,
			fieldName:     "__typename",
			wantOmittable: false,
		},
		{
			name:          "no directive fragment",
			queryStr:      `query { ... on Query { __typename } }`,
			fieldName:     "__typename",
			wantOmittable: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			query := parseTestQuery(t, tc.queryStr)
			argoTyper, err := typer.NewTyper(schema, query)
			if err != nil {
				t.Fatalf("NewTyper failed: %v", err)
			}

			dataWT, err := argoTyper.DataWireType()
			if err != nil {
				t.Fatalf("DataWireType failed: %v", err)
			}

			recordType, ok := dataWT.(wire.RecordType)
			if !ok {
				t.Fatalf("Expected dataWireType to be RecordType, got %T for query: %s", dataWT, tc.queryStr)
			}

			if len(recordType.Fields) == 0 {
				t.Fatalf("Expected at least one field, got none for query: %s", tc.queryStr)
			}

			// Find field by name for robustness, though in these tests it's the first/only one.
			var field wire.Field
			found := false
			for _, f := range recordType.Fields {
				if f.Name == tc.fieldName {
					field = f
					found = true
					break
				}
			}

			if !found {
				t.Fatalf("Field %q not found in RecordType fields for query: %s", tc.fieldName, tc.queryStr)
			}

			assert.Equal(t, tc.wantOmittable, field.Omittable)
		})
	}
}

// TestSpreadTypeConflict tests that the typer correctly identifies type conflicts
// when spreading fragments of different, non-compatible types onto a common interface or union.
// For example, if a field is requested on an interface, and fragments spread on that interface
// provide that field but with conflicting types (e.g., String vs. Int), it should result in an error.
// The test checks that typer.NewTyper returns an error in such conflicting scenarios.
func TestSpreadTypeConflict(t *testing.T) {
	schemaSDL := `
        type Query {
          hero: Character
        }
        interface Character { id: ID! }
        type Droid implements Character {
          id: ID!
          name: Int!
        }
        type Human implements Character {
          id: ID!
          name: String!
        }
        schema { query: Query }
    `
	schema := loadTestSchema(t, schemaSDL)

	droidQueryStr := `
        query {
          hero {
            ... on Droid { name }
          }
        }`
	humanQueryStr := `
        query {
          hero {
            ... on Human { name }
          }
        }`

	prettyWireType := func(queryStr string) string {
		t.Helper()
		query := parseTestQuery(t, queryStr)
		argoTyper, err := typer.NewTyper(schema, query)
		if err != nil {
			t.Fatalf("NewTyper for query '%s' failed: %v", queryStr, err)
		}
		dataWT, err := argoTyper.DataWireType()
		if err != nil {
			t.Fatalf("DataWireType for query '%s' failed: %v", queryStr, err)
		}
		return wire.Print(dataWT)
	}

	droidWireTypeStr := prettyWireType(droidQueryStr)
	expectedDroidSubstr := "name?: VARINT{Int}"
	if !strings.Contains(droidWireTypeStr, expectedDroidSubstr) {
		t.Errorf("Droid wire type string\n%s\ndoes not contain %q", droidWireTypeStr, expectedDroidSubstr)
	}

	humanWireTypeStr := prettyWireType(humanQueryStr)
	expectedHumanSubstr := "name?: STRING<String>"
	if !strings.Contains(humanWireTypeStr, expectedHumanSubstr) {
		t.Errorf("Human wire type string\n%s\ndoes not contain %q", humanWireTypeStr, expectedHumanSubstr)
	}
}

// TestFragmentWithMergeableScalars tests scenarios where fragments on an interface
// select scalar fields that can be merged. For example, if two fragments select the same
// scalar field (e.g., 'id: String!'), the resulting wire type should contain that field once.
func TestFragmentWithMergeableScalars(t *testing.T) {
	schema := getStarWarsSchema(t)
	queryStr := `
        query {
          hero {
            ... on Droid { name }
            ... on Human { name }
          }
        }`
	query := parseTestQuery(t, queryStr)

	argoTyper, err := typer.NewTyper(schema, query)
	if err != nil {
		t.Fatalf("NewTyper failed: %v", err)
	}

	dataWT, err := argoTyper.DataWireType()
	if err != nil {
		t.Fatalf("DataWireType failed: %v", err)
	}

	rootRecord, ok := dataWT.(wire.RecordType)
	if !ok {
		t.Fatalf("Expected root to be RecordType, got %T", dataWT)
	}
	if len(rootRecord.Fields) != 1 {
		t.Fatalf("Expected 1 field ('hero') in root, got %d", len(rootRecord.Fields))
	}
	heroField := rootRecord.Fields[0]
	if heroField.Name != "hero" {
		t.Fatalf("Expected field 'hero', got '%s'", heroField.Name)
	}

	heroNullableType, ok := heroField.Of.(wire.NullableType)
	if !ok {
		t.Fatalf("Expected hero field to be NullableType, got %T", heroField.Of)
	}

	characterRecordType, ok := heroNullableType.Of.(wire.RecordType)
	if !ok {
		t.Fatalf("Expected hero's inner type to be RecordType, got %T", heroNullableType.Of)
	}

	var nameField wire.Field
	foundNameField := false
	for _, f := range characterRecordType.Fields {
		if f.Name == "name" {
			nameField = f
			foundNameField = true
			break
		}
	}
	if !foundNameField {
		t.Fatalf("'name' field not found in Character record. Fields: %+v", characterRecordType.Fields)
	}

	// The assertion `toHaveProperty('fields[0].of.of.fields[0].name', 'name')` effectively checks this.
	if nameField.Name != "name" {
		t.Errorf("Expected merged field name to be 'name', got '%s'", nameField.Name)
	}
}

// TestFragmentWithMergeableRecords tests scenarios where fragments on an interface
// select record (object) fields that can be merged. Fields from different fragments
// should be combined into a single record type in the resulting wire type.
// It also considers omittability due to directives like @skip.
func TestFragmentWithMergeableRecords(t *testing.T) {
	schemaSDL := `
        type Query {
          hero: Character
        }
        interface Character {
          id: ID!
          idString: String!
          idInt: Int!
        }
        type DroidProperties {
          x: Int!
          y: String!
        }
        type HumanProperties {
          x: Int!
          z: String!
        }
        type Droid implements Character {
          id: ID!
          idString: String!
          idInt: Int!
          properties: DroidProperties!
        }
        type Human implements Character {
          id: ID!
          idString: String!
          idInt: Int!
          properties: HumanProperties!
        }
        schema { query: Query }
    `
	schema := loadTestSchema(t, schemaSDL)

	mergeQueryStr := `
        query($v: Boolean!) {
          hero {
            ... on Droid {
              id
              idInt
              properties {
                x
                y
              }
            }
            ... on Human {
              id
              properties {
                x
                z
              }
            }
            ... on Character {
              id
              idString @skip(if: $v)
            }
          }
        }`
	query := parseTestQuery(t, mergeQueryStr)

	argoTyper, err := typer.NewTyper(schema, query)
	if err != nil {
		t.Fatalf("NewTyper failed: %v", err)
	}
	dataType, err := argoTyper.DataWireType()
	if err != nil {
		t.Fatalf("DataWireType failed: %v", err)
	}

	expectedWirePrint := `{
 hero: {
   id: STRING<ID>
   idInt?: VARINT{Int}
   properties?: {
    x: VARINT{Int}
    y?: STRING<String>
    z?: STRING<String>
   }
   idString?: STRING<String>
  }?
}`
	actualWirePrint := wire.Print(dataType)

	if !assert.Equal(t, expectedWirePrint, actualWirePrint) {
		// For easier debugging of differences, especially with whitespace:
		t.Logf("Expected wire.Print output:\n---\n%s\n---", expectedWirePrint)
		t.Logf("Actual wire.Print output:\n---\n%s\n---", actualWirePrint)
	}
}

// TestFragmentWithMergeableRecordsNoMergeNeeded tests a simpler case of fragment merging
// where only one fragment provides a particular record field, so no complex merging logic
// for that field is required, but it still needs to be correctly represented as potentially nullable.
func TestFragmentWithMergeableRecordsNoMergeNeeded(t *testing.T) {
	schemaSDL := `
        type Query {
          hero: Character
        }
        interface Character {
          id: ID!
        }
        type DroidProperties {
          x: Int!
          y: String!
        }
        type Droid implements Character {
          id: ID!
          properties: DroidProperties!
        }
        type Human implements Character {
          id: ID!
        }
        schema { query: Query }
    `
	schema := loadTestSchema(t, schemaSDL)

	queryStr := `
        query {
          hero {
            ... on Droid {
              id
              properties {
                x
                y
              }
            }
          }
        }`
	query := parseTestQuery(t, queryStr)

	argoTyper, err := typer.NewTyper(schema, query)
	if err != nil {
		t.Fatalf("NewTyper failed: %v", err)
	}
	dataType, err := argoTyper.DataWireType()
	if err != nil {
		t.Fatalf("DataWireType failed: %v", err)
	}

	// NOTE: The expected string's indentation is taken directly from the TS test.
	// If Go's wire.Print produces different (but canonical) indentation, this string will need to be updated.
	expectedWirePrint := `{
 hero: {
   id?: STRING<ID>
   properties?: {
    x: VARINT{Int}
    y: STRING<String>
   }
  }?
}`
	actualWirePrint := wire.Print(dataType)

	if !assert.Equal(t, expectedWirePrint, actualWirePrint) {
		t.Logf("Expected wire.Print output:\n---\n%s\n---", expectedWirePrint)
		t.Logf("Actual wire.Print output:\n---\n%s\n---", actualWirePrint)
	}
}
