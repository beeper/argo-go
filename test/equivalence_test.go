// Package argo_test contains equivalence tests for the Argo library,
// comparing its behavior against reference implementations or expected outputs
// based on various schemas and queries.
package argo_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/elliotchance/orderedmap/v3"
	"github.com/vektah/gqlparser/v2"

	"github.com/beeper/argo-go/pkg/bitset"
	"github.com/beeper/argo-go/wire"

	"github.com/beeper/argo-go"
	"github.com/beeper/argo-go/codec"
	"github.com/beeper/argo-go/header"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// baseTestDir is the root directory for equivalence test data files.
var baseTestDir = filepath.Join("equivalence")

// StarWarsSchemaText holds the raw GraphQL schema string for Star Wars tests.
// It is loaded once during package initialization.
var starWarsSchemaText string

// StarWarsSchema is the parsed *ast.Schema for Star Wars tests.
// It is loaded once during package initialization.
var StarWarsSchema *ast.Schema

func init() {
	swSchemaPath := filepath.Join(baseTestDir, "starwars", "schema.graphql")
	schemaBytes, err := os.ReadFile(swSchemaPath)
	if err != nil {
		panic(fmt.Sprintf("failed to read StarWars schema file %s: %v", swSchemaPath, err))
	}
	starWarsSchemaText = string(schemaBytes)

	StarWarsSchema, err = gqlparser.LoadSchema(&ast.Source{Name: filepath.Base(swSchemaPath), Input: starWarsSchemaText, BuiltIn: false})
	if err != nil {
		panic(fmt.Sprintf("failed to parse StarWars schema: %v", err))
	}
}

// slurp reads the entire content of a file specified by its path into a string.
// It panics if the file cannot be read.
func slurp(file string) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// TestData holds all necessary information for a single equivalence test case.
// It includes the schema, query, expected results (both as a map and JSON string),
// and identifying information like directory and name.
type TestData struct {
	Dir      string                                      // Directory of the test case, relative to baseTestDir
	Name     string                                      // Name of the test case (e.g., file name without extension)
	Schema   *ast.Schema                                 // Parsed GraphQL schema for this test case
	Query    *ast.QueryDocument                          // Parsed GraphQL query document
	Expected *orderedmap.OrderedMap[string, interface{}] // Expected result, unmarshalled into an ordered map for comparison
	JSON     string                                      // Expected result as an indented JSON string (maintains order from source .json file)
}

// listSubDirs recursively finds all subdirectories within the given root directory.
// It skips any directory whose name contains "disabled".
// The returned paths are relative to the initial root.
func listSubDirs(root string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root {
			if strings.Contains(d.Name(), "disabled") {
				return filepath.SkipDir
			}
			dirs = append(dirs, path)
		}
		return nil
	})
	return dirs, err
}

// listFilesNonRecursive lists all files (not directories) directly within a single given directory.
// It skips any file whose name contains "disabled".
func listFilesNonRecursive(dir string) ([]string, error) {
	var files []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			if strings.Contains(entry.Name(), "disabled") {
				continue
			}
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	return files, nil
}

// loadTestCases discovers and loads all test data by scanning subdirectories of the given root directory.
// It uses loadTestDataFromDir for each subdirectory. If no subdirectories are found,
// it attempts to load test cases directly from the root directory itself.
func loadTestCases(t *testing.T, dir string) []TestData {
	var allLoadedTests []TestData

	subDirs, err := listSubDirs(dir)
	require.NoError(t, err, "Failed to list subdirectories for %s", dir)

	for _, testCaseDir := range subDirs {
		testsFromDir, err := loadTestDataFromDir(t, testCaseDir)
		if err != nil {
			t.Logf("Skipping directory %s due to error: %v", testCaseDir, err)
			continue
		}
		allLoadedTests = append(allLoadedTests, testsFromDir...)
	}

	if len(allLoadedTests) == 0 && len(subDirs) == 0 {
		t.Logf("No tests found in subdirectories of %s, attempting to load from %s itself.", dir, dir)
		testsFromRootDir, err := loadTestDataFromDir(t, dir)
		if err == nil {
			allLoadedTests = append(allLoadedTests, testsFromRootDir...)
		} else {
			t.Logf("Failed to load tests from root directory %s: %v", dir, err)
		}
	}
	return allLoadedTests
}

// loadTestDataFromDir loads all test data (schema, queries, and expected results)
// from a single specified directory. It expects a 'schema.graphql' file and pairs of
// '.graphql' query files and corresponding '.json' result files.
// Files/directories containing "disabled" are skipped.
func loadTestDataFromDir(t *testing.T, dir string) ([]TestData, error) {
	var testsInDir []TestData

	schemaPath := filepath.Join(dir, "schema.graphql")
	schemaSrc, err := slurp(schemaPath)
	if err != nil {
		return nil, fmt.Errorf("schema.graphql not found or not readable in %s: %w", dir, err)
	}
	currentSchema, err := gqlparser.LoadSchema(&ast.Source{Name: "schema.graphql", Input: schemaSrc})
	if err != nil {
		return nil, fmt.Errorf("failed to parse schema in %s: %w", dir, err)
	}

	queries := make(map[string]*ast.QueryDocument)
	results := make(map[string]*orderedmap.OrderedMap[string, interface{}])

	filesInDir, err := listFilesNonRecursive(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to list files in %s: %w", dir, err)
	}

	for _, filePath := range filesInDir {
		baseName := filepath.Base(filePath)
		ext := filepath.Ext(baseName)
		nameOnly := strings.TrimSuffix(baseName, ext)

		if baseName == "schema.graphql" || strings.HasSuffix(baseName, ".argo") || strings.HasSuffix(baseName, ".wire") {
			continue
		}

		switch ext {
		case ".graphql":
			querySrc, err := slurp(filePath)
			require.NoError(t, err, "Failed to read query file %s", filePath)
			doc, gqlErr := parser.ParseQuery(&ast.Source{Name: baseName, Input: querySrc})
			require.Nil(t, gqlErr, "ParseQuery failed for %s: %v", filePath, gqlErr)
			queries[nameOnly] = doc
		case ".json":
			jsonSrcBytes, err := os.ReadFile(filePath)
			require.NoError(t, err, "Failed to read result file %s", filePath)
			res, err := unmarshalJsonToOrderedMapRecursive(jsonSrcBytes)
			require.NoError(t, err, "Recursive JSON unmarshal to OrderedMap failed for %s", filePath)
			results[nameOnly] = res
		default:
			t.Logf("Warning: Got unexpected file in test directory %s: %s", dir, filePath)
		}
	}

	if len(queries) == 0 && len(results) == 0 {
		return nil, nil
	}
	require.Greater(t, len(queries), 0, "No query files (.graphql) found in test directory: %s", dir)

	queryNames := make([]string, 0, len(queries))
	for name := range queries {
		queryNames = append(queryNames, name)
	}
	sort.Strings(queryNames)

	resultNames := make([]string, 0, len(results))
	for name := range results {
		resultNames = append(resultNames, name)
	}
	sort.Strings(resultNames)
	require.Equal(t, queryNames, resultNames, "Mismatch between .graphql and .json file names in %s", dir)

	for name, queryDoc := range queries {
		expectedResult, ok := results[name]
		require.True(t, ok, "Result .json file not found for query %s in %s", name, dir)

		jsonBytes, err := json.MarshalIndent(expectedResult, "", "  ")
		require.NoError(t, err)

		testsInDir = append(testsInDir, TestData{
			Dir:      dir,
			Name:     name,
			Schema:   currentSchema,
			Query:    queryDoc,
			Expected: expectedResult,
			JSON:     string(jsonBytes),
		})
	}
	return testsInDir, nil
}

// TestStarWarsEquivalence runs equivalence tests specifically for the Star Wars schema and queries.
// It loads test cases from the 'starwars' subdirectory and executes runEquivalenceLogic for each.
func TestStarWarsEquivalence(t *testing.T) {
	starwarsDir := filepath.Join(baseTestDir, "starwars")
	testCases := loadTestCases(t, starwarsDir)
	require.NotEmpty(t, testCases, "No test cases found for Star Wars")

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			t.Log("============================================================\nRunning Star Wars test:", tc.Name)
			runEquivalenceLogic(t, tc)
		})
	}
}

// TestQueriesEquivalence runs equivalence tests for general queries, excluding those
// specifically designated for Star Wars (by convention, those in a "starwars" subdirectory).
// It loads all test cases from the baseTestDir and filters out Star Wars cases.
func TestQueriesEquivalence(t *testing.T) {
	testCases := loadTestCases(t, baseTestDir)
	require.NotEmpty(t, testCases, "No general test cases found (excluding Star Wars)")

	var generalTestCases []TestData
	for _, tc := range testCases {
		if !strings.Contains(strings.ToLower(filepath.ToSlash(tc.Dir)), "starwars") {
			generalTestCases = append(generalTestCases, tc)
		}
	}
	require.NotEmpty(t, generalTestCases, "No general test cases found after filtering Star Wars")

	for _, tc := range generalTestCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			t.Log("============================================================\nRunning general test:", tc.Name)
			runEquivalenceLogic(t, tc)
		})
	}
}

// TestTyperBehavior checks the implicit typing and schema analysis performed by NewExecutionResultCodec.
// It uses Star Wars test cases as a basis, attempting to create an ExecutionResultCodec
// for each and failing if any schema or query incompatibilities are detected during codec initialization.
func TestTyperBehavior(t *testing.T) {
	starwarsDir := filepath.Join(baseTestDir, "starwars")
	testCases := loadTestCases(t, starwarsDir)
	require.NotEmpty(t, testCases, "No test cases found for Typer behavior test (uses Star Wars data)")

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			var operationName string
			if len(tc.Query.Operations) > 0 {
				operationName = tc.Query.Operations[0].Name
			}
			_, err := argo.NewExecutionResultCodec(tc.Schema, tc.Query, operationName)
			require.NoError(t, err, "NewExecutionResultCodec failed for %s, implying a typing issue", tc.Name)
		})
	}
}

// runEquivalenceLogic contains the core test logic for a single equivalence test case (TestData).
// It creates an ExecutionResultCodec, compares the generated wire type against an expected .wire file,
// encodes the expected JSON data to Argo bytes, decodes those bytes back to JSON,
// and then compares the reconstructed JSON with the original expected JSON.
// This process is run with different encoder configurations
func runEquivalenceLogic(t *testing.T, tc TestData) {
	var operationName string
	if len(tc.Query.Operations) > 0 && tc.Query.Operations[0] != nil {
		operationName = tc.Query.Operations[0].Name
	}

	erc, err := argo.NewExecutionResultCodec(tc.Schema, tc.Query, operationName)
	require.NoError(t, err)

	t.Run("WireType", func(t *testing.T) {
		wireFileName := fmt.Sprintf("%s.wire", tc.Name)
		wireFilePath := filepath.Join(tc.Dir, wireFileName)

		expectedWireBytes, errWireFile := os.ReadFile(wireFilePath)
		require.NoError(t, errWireFile)

		rootType, err := erc.Typer().RootWireType()
		require.NoError(t, err)
		require.Equal(t, string(expectedWireBytes), wire.Print(rootType))
	})

	// --- Test with different encoder options ---
	runWithOptions := func(optDesc string, configureEncoder func(enc *codec.ArgoEncoder)) {
		t.Run(optDesc, func(t *testing.T) {
			encoder := codec.NewArgoEncoder()
			configureEncoder(encoder)

			encoder.Debug = true

			argoBytes, errEnc := erc.OrderedMapToArgoWithEncoder(tc.Expected, encoder)
			require.NoError(t, errEnc)
			require.NotNil(t, argoBytes, "OrderedMapToArgoWithEncoder returned nil argoBytes")

			// --- Gold file comparison ---
			t.Run("GoldBytes", func(t *testing.T) {
				argoBytes.SetPosition(0)
				actualArgoRawBytes := argoBytes.Bytes()
				require.True(t, len(actualArgoRawBytes) > 0, "Generated Argo bytes are empty for test %s, options %s", tc.Name, optDesc)

				headerBytes, err := encoder.Header().AsBytes()
				require.NoError(t, err, "Failed to read header bytes")
				headerHex := hex.EncodeToString(headerBytes)

				goldFileName := fmt.Sprintf("%s-%s.argo", tc.Name, headerHex)
				goldFilePath := filepath.Join(tc.Dir, goldFileName)
				t.Logf("Test %s, options %s: expecting gold file %s (Go-generated headerBytes for filename: %s)", tc.Name, optDesc, goldFilePath, headerHex)
				expectedArgoBytes, errGoldFile := os.ReadFile(goldFilePath)
				if errGoldFile != nil {
					// You might want to temporarily write the Go output for debugging if a gold file is missing
					// tempActualFile := filepath.Join(tc.Dir, fmt.Sprintf("%s-%s.actual-go.argo", tc.Name, headerBytes))
					// os.WriteFile(tempActualFile, actualArgoRawBytes, 0644)
					// t.Logf("Actual Go output for %s-%s written to %s for debugging", tc.Name, headerBytes, tempActualFile)
				}
				require.NoError(t, errGoldFile, "Failed to read gold file %s. Check if JS test generated it or if headerBytes (%s) is correct.", goldFilePath, headerHex)

				t.Run("Encode", func(t *testing.T) {
					assert.Equal(t, expectedArgoBytes, actualArgoRawBytes, "Go-generated Argo bytes do not match gold file %s", goldFilePath)
				})

				t.Run("Decode", func(t *testing.T) {
					argoBytes.SetPosition(0)
					fromArgoResultMap, errArgoMap := erc.ArgoToOrderedMap(argoBytes)
					require.NoError(t, errArgoMap)

					fromArgoResultJson, errJson := json.MarshalIndent(fromArgoResultMap, "", "  ")
					require.NoError(t, errJson)
					if !assert.JSONEq(t, tc.JSON, string(fromArgoResultJson)) {
						t.Logf("JSON mismatch with encoder options '%s' for test %s", optDesc, tc.Name)
						t.Logf("Expected JSON:\n%s\n", tc.JSON)
						t.Logf("Actual JSON from Argo:\n%s\n", string(fromArgoResultJson))
					}
				})
			})

			t.Run("Json", func(t *testing.T) {
				argoBytes.SetPosition(0)
				fromArgoResultMap, errArgoMap := erc.ArgoToOrderedMap(argoBytes)
				require.NoError(t, errArgoMap)

				argoToJsonBytes, errJson := json.MarshalIndent(fromArgoResultMap, "", "  ")
				require.NoError(t, errJson)

				if !assert.JSONEq(t, tc.JSON, string(argoToJsonBytes)) {
					t.Logf("JSON mismatch with encoder options '%s' for test %s", optDesc, tc.Name)
					t.Logf("Expected JSON:\n%s\n", tc.JSON)
					t.Logf("Actual JSON from Argo:\n%s\n", string(argoToJsonBytes))
				}
			})
		})
	}

	runWithOptions("DefaultFlags", func(enc *codec.ArgoEncoder) {
		// these headers are actually always set, due to their opposites not being implemented
		enc.Header().SetFlag(header.HeaderOutOfBandFieldErrorsFlag, true)
		enc.Header().SetFlag(header.HeaderSelfDescribingErrorsFlag, true)
	})
	runWithOptions("InlineEverything", func(enc *codec.ArgoEncoder) {
		enc.Header().SetFlag(header.HeaderInlineEverythingFlag, true)
	})
	runWithOptions("SelfDescribing", func(enc *codec.ArgoEncoder) {
		enc.Header().SetFlag(header.HeaderSelfDescribingFlag, true)
	})
	runWithOptions("InlineEverythingAndSelfDescribing", func(enc *codec.ArgoEncoder) {
		enc.Header().SetFlag(header.HeaderInlineEverythingFlag, true)
		enc.Header().SetFlag(header.HeaderSelfDescribingFlag, true)
	})
	runWithOptions("NullTerminatedStrings", func(enc *codec.ArgoEncoder) {
		enc.Header().SetFlag(header.HeaderNullTerminatedStringsFlag, true)
	})
	runWithOptions("NoDeduplication", func(enc *codec.ArgoEncoder) {
		enc.Header().SetFlag(header.HeaderNoDeduplicationFlag, true)
	})
	runWithOptions("UserFlags(bit 5)", func(enc *codec.ArgoEncoder) {
		userFlags := bitset.NewBitSet()
		userFlags.SetBit(5)
		enc.Header().SetUserFlags(userFlags)
	})
}

// TestMain performs global setup and teardown for the equivalence tests.
// It checks for the existence of the baseTestDir and ensures the StarWarsSchema is loaded.
// It then runs all tests in the package and exits with the appropriate code.
func TestMain(m *testing.M) {
	if _, err := os.Stat(baseTestDir); os.IsNotExist(err) {
		fmt.Printf("FATAL: Base test directory %s does not exist.\n", baseTestDir)
		fmt.Println("Please ensure test data (schemas, queries, results) is populated under ./equivalence/ (or the path configured in baseTestDir)")
		os.Exit(1)
	}
	if StarWarsSchema == nil {
		fmt.Println("FATAL: StarWarsSchema failed to load. Check init() and schema file path.")
		os.Exit(1)
	}
	exitCode := m.Run()
	os.Exit(exitCode)
}

// --- Helper functions for ordered map and JSON conversion ---

// unmarshalJsonToOrderedMapRecursive unmarshals a JSON byte slice into an *orderedmap.OrderedMap[string, interface{}],
// preserving the order of keys from the JSON object. It recursively handles nested objects and arrays.
func unmarshalJsonToOrderedMapRecursive(data []byte) (*orderedmap.OrderedMap[string, interface{}], error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	t, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("error reading initial token: %w", err)
	}
	if delim, ok := t.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("expected JSON object at top level, got %T: %v", t, t)
	}
	return parseJsonObjectRecursive(dec)
}

// parseJsonObjectRecursive parses a JSON object from a json.Decoder into an *orderedmap.OrderedMap,
// preserving key order and recursively parsing nested values.
// Assumes the initial '{' token has already been consumed.
func parseJsonObjectRecursive(dec *json.Decoder) (*orderedmap.OrderedMap[string, interface{}], error) {
	om := orderedmap.NewOrderedMap[string, interface{}]()
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("error reading json object key token: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("expected string key, got %T: %v", keyToken, keyToken)
		}

		value, err := parseJsonValueRecursive(dec)
		if err != nil {
			return nil, fmt.Errorf("error parsing value for key '%s': %w", key, err)
		}
		om.Set(key, value)
	}
	t, err := dec.Token()
	if err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("json object ended unexpectedly: %w", io.ErrUnexpectedEOF)
		}
		return nil, fmt.Errorf("error reading json object close token: %w", err)
	}
	if delim, ok := t.(json.Delim); !ok || delim != '}' {
		return nil, fmt.Errorf("expected JSON object close delimiter '}', got %T: %v", t, t)
	}
	return om, nil
}

// parseJsonArrayRecursive parses a JSON array from a json.Decoder into a []interface{},
// recursively parsing its elements.
// Assumes the initial '[' token has already been consumed.
func parseJsonArrayRecursive(dec *json.Decoder) ([]interface{}, error) {
	var arr []interface{}
	for dec.More() {
		val, err := parseJsonValueRecursive(dec)
		if err != nil {
			return nil, fmt.Errorf("error parsing json array element: %w", err)
		}
		arr = append(arr, val)
	}
	t, err := dec.Token()
	if err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("json array ended unexpectedly: %w", io.ErrUnexpectedEOF)
		}
		return nil, fmt.Errorf("error reading json array close token: %w", err)
	}
	if delim, ok := t.(json.Delim); !ok || delim != ']' {
		return nil, fmt.Errorf("expected JSON array close delimiter ']', got %T: %v", t, t)
	}
	return arr, nil
}

// parseJsonValueRecursive parses a single JSON value (object, array, string, number, boolean, or null)
// from a json.Decoder, handling recursive parsing for objects and arrays.
func parseJsonValueRecursive(dec *json.Decoder) (interface{}, error) {
	t, err := dec.Token()
	if err != nil {
		if err == io.EOF {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("error reading json value token: %w", err)
	}

	switch token := t.(type) {
	case json.Delim:
		switch token {
		case '{':
			return parseJsonObjectRecursive(dec)
		case '[':
			return parseJsonArrayRecursive(dec)
		default:
			return nil, fmt.Errorf("unexpected json delimiter token: %v", token)
		}
	case bool, float64, string, nil:
		return token, nil
	default:
		return nil, fmt.Errorf("unexpected json token type: %T, value: %v", token, token)
	}
}

// orderedMapToMapRecursive recursively converts an *orderedmap.OrderedMap[string, interface{}]
// into a standard map[string]interface{}. Nested ordered maps and slices are also converted.
func orderedMapToMapRecursive(om *orderedmap.OrderedMap[string, interface{}]) map[string]interface{} {
	if om == nil {
		return nil
	}
	m := make(map[string]interface{}, om.Len())
	for el := om.Front(); el != nil; el = el.Next() {
		key := el.Key
		value := el.Value

		if nestedOM, ok := value.(*orderedmap.OrderedMap[string, interface{}]); ok {
			m[key] = orderedMapToMapRecursive(nestedOM)
		} else if nestedSlice, ok := value.([]interface{}); ok {
			m[key] = convertSliceToPlainMapsRecursive(nestedSlice)
		} else {
			m[key] = value
		}
	}
	return m
}

// convertSliceToPlainMapsRecursive recursively traverses a slice, converting any
// *orderedmap.OrderedMap[string, interface{}] elements into map[string]interface{}.
func convertSliceToPlainMapsRecursive(s []interface{}) []interface{} {
	res := make([]interface{}, len(s))
	for i, v := range s {
		if nestedOM, ok := v.(*orderedmap.OrderedMap[string, interface{}]); ok {
			res[i] = orderedMapToMapRecursive(nestedOM)
		} else if nestedSlice, ok := v.([]interface{}); ok {
			res[i] = convertSliceToPlainMapsRecursive(nestedSlice)
		} else {
			res[i] = v
		}
	}
	return res
}

// mapToOrderedMapRecursive recursively converts a map[string]interface{}
// into an *orderedmap.OrderedMap[string, interface{}]. Keys are sorted alphabetically
// in the resulting ordered map. Nested maps and slices are also converted.
func mapToOrderedMapRecursive(m map[string]interface{}) *orderedmap.OrderedMap[string, interface{}] {
	if m == nil {
		return nil
	}
	om := orderedmap.NewOrderedMap[string, interface{}]()
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		value := m[k]
		if nestedMap, ok := value.(map[string]interface{}); ok {
			om.Set(k, mapToOrderedMapRecursive(nestedMap))
		} else if nestedSlice, ok := value.([]interface{}); ok {
			om.Set(k, convertSliceToOrderedMapsRecursive(nestedSlice))
		} else {
			om.Set(k, value)
		}
	}
	return om
}

// convertSliceToOrderedMapsRecursive recursively traverses a slice, converting any
// map[string]interface{} elements into *orderedmap.OrderedMap[string, interface{}].
func convertSliceToOrderedMapsRecursive(s []interface{}) []interface{} {
	res := make([]interface{}, len(s))
	for i, v := range s {
		if nestedMap, ok := v.(map[string]interface{}); ok {
			res[i] = mapToOrderedMapRecursive(nestedMap)
		} else if nestedSlice, ok := v.([]interface{}); ok {
			res[i] = convertSliceToOrderedMapsRecursive(nestedSlice)
		} else {
			res[i] = v
		}
	}
	return res
}
