// Package typer is responsible for converting GraphQL schema types and query
// selections into Argo wire types. It processes GraphQL operations (queries,
// mutations, subscriptions) and their associated fragments to determine the static
// Argo type structure for the expected response, including handling of Argo-specific
// directives for codec and deduplication control.
package typer

import (
	"fmt"

	"github.com/elliotchance/orderedmap/v3"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/beeper/argo-go/internal/util"
	argoDirs "github.com/beeper/argo-go/pkg/directives"
	"github.com/beeper/argo-go/wire"
)

// SelectedFieldNode represents a GraphQL field that is part of a selection set,
// along with the AST node (Field, FragmentSpread, or InlineFragment) that caused
// it to be selected. This is used during the collection of fields to track the
// origin of each selected field, which is important for resolving type conditions
// and merging fields from different paths.
type SelectedFieldNode struct {
	SelectedBy ast.Selection // The AST node (Field, FragmentSpread, InlineFragment) that selected this field.
	Field      *ast.Field    // The actual ast.Field that was selected.
}

// Typer translates GraphQL schema definitions and query documents into Argo wire types.
// It holds the context of the current GraphQL schema, the query document being processed,
// fragment definitions, and the resolved Argo wire types for various parts of the query.
// The Typer focuses on a single operation within the query document.
type Typer struct {
	schema    *ast.Schema                                             // The loaded GraphQL schema.
	query     *ast.QueryDocument                                      // The input GraphQL query document.
	fragments *orderedmap.OrderedMap[string, *ast.FragmentDefinition] // Parsed fragment definitions from the query document.
	// types maps the starting position of an ast.Field in the query to its determined wire.Type.
	// This allows looking up the Argo type for specific field occurrences in the query AST.
	types     *orderedmap.OrderedMap[int, wire.Type] // Key is ast.Field.Position.Start
	operation *ast.OperationDefinition               // The specific operation (query, mutation, subscription) within the document being processed.
}

// ArgoDirectives lists the custom Argo-specific directives that the Typer recognizes and processes.
var ArgoDirectives = []*ast.DirectiveDefinition{
	argoDirs.ArgoCodecDirective,
	argoDirs.ArgoDeduplicateDirective,
}

// Constants for GraphQL introspection field names.
const (
	schemaMetaFieldName   = "__schema"
	typeMetaFieldName     = "__type"
	typeNameMetaFieldName = "__typename"
)

// Definitions for GraphQL introspection meta-fields.
var (
	typeNameMetaFieldDef = &ast.FieldDefinition{
		Name:        typeNameMetaFieldName,
		Description: "The name of the current Object type at runtime.",
		Type:        ast.NonNullNamedType("String", nil),
	}
	schemaMetaFieldDef = &ast.FieldDefinition{
		Name:        schemaMetaFieldName,
		Description: "Access the current type schema of this server.",
		Type:        ast.NonNullNamedType("__Schema", nil), // Note: __Schema is a type, not a scalar string
	}
	typeMetaFieldDef = &ast.FieldDefinition{
		Name:        typeMetaFieldName,
		Description: "Request the type information of a single type.",
		Type:        ast.NamedType("__Type", nil), // Note: __Type is a type
		Arguments: ast.ArgumentDefinitionList{
			{
				Name: "name",
				Type: ast.NonNullNamedType("String", nil),
			},
		},
	}
)

// NewTyper creates a new Typer for a given GraphQL schema, query document, and operation name.
// If the query document contains multiple operations, an operationName must be provided.
// It initializes fragment definitions and identifies the target operation.
func NewTyper(s *ast.Schema, q *ast.QueryDocument, operationName ...string) (*Typer, error) {
	t := &Typer{
		schema:    s,
		query:     q,
		fragments: orderedmap.NewOrderedMap[string, *ast.FragmentDefinition](),
		types:     orderedmap.NewOrderedMap[int, wire.Type](),
	}

	opName := ""
	if len(operationName) > 0 {
		opName = operationName[0]
	}

	if len(q.Operations) == 0 {
		return nil, gqlerror.Errorf("No operations found in query document")
	}

	if opName == "" { // No operation name specified
		if len(q.Operations) > 1 {
			return nil, gqlerror.Errorf("Must provide operation name if query contains multiple operations")
		}
		t.operation = q.Operations[0]
	} else { // Operation name specified
		found := false
		for _, opDef := range q.Operations {
			if opDef.Name == opName {
				t.operation = opDef
				found = true
				break
			}
		}
		if !found {
			return nil, gqlerror.Errorf("Operation %q not found in query document", opName)
		}
	}

	for _, fragDef := range q.Fragments {
		t.fragments.Set(fragDef.Name, fragDef)
	}

	return t, nil
}

// getSelectionDirectives is a utility function to extract the directive list
// from any ast.Selection node (Field, FragmentSpread, or InlineFragment).
func getSelectionDirectives(s ast.Selection) ast.DirectiveList {
	switch sel := s.(type) {
	case *ast.Field:
		return sel.Directives
	case *ast.FragmentSpread:
		return sel.Directives
	case *ast.InlineFragment:
		return sel.Directives
	}
	return nil
}

// Operation returns the specific *ast.OperationDefinition that this Typer instance is processing.
func (t *Typer) Operation() *ast.OperationDefinition {
	return t.operation
}

// RootType determines the root GraphQL object type (e.g., Query, Mutation, Subscription)
// for the current operation being processed by the Typer.
func (t *Typer) RootType() (*ast.Definition, error) {
	var typeDef *ast.Definition
	switch t.operation.Operation {
	case ast.Query:
		typeDef = t.schema.Query
	case ast.Mutation:
		typeDef = t.schema.Mutation
	case ast.Subscription:
		typeDef = t.schema.Subscription
	default:
		return nil, gqlerror.Errorf("Unsupported operation type: %s", t.operation.Operation)
	}
	if typeDef == nil {
		return nil, gqlerror.Errorf("Schema does not define a root type for operation %s", t.operation.Operation)
	}
	return typeDef, nil
}

// RootWireType computes the top-level Argo wire.Type for the entire GraphQL response,
// which includes both the 'data' and 'errors' fields, according to the GraphQL specification.
func (t *Typer) RootWireType() (wire.Type, error) {
	dataWT, err := t.DataWireType()
	if err != nil {
		return nil, err
	}
	fields := []wire.Field{
		{Name: "data", Of: wire.NewNullableType(dataWT), Omittable: false},                         // Data can be null if errors occur.
		{Name: "errors", Of: wire.NewNullableType(wire.ArrayType{Of: wire.Desc}), Omittable: true}, // Errors is omittable if no errors.
	}
	return wire.RecordType{Fields: fields}, nil
}

// DataWireType computes the Argo wire.Type specifically for the 'data' part of a GraphQL response,
// based on the operation's selection set against the root operation type.
func (t *Typer) DataWireType() (wire.Type, error) {
	rootType, err := t.RootType()
	if err != nil {
		return nil, err
	}
	getField := t.makeGetField(rootType)
	return t.collectFieldWireTypes(rootType, t.operation.SelectionSet, getField)
}

// collectFieldsStatic gathers all fields selected by a selection set, resolving fragment spreads
// and inline fragments, and groups them by their response key (alias or field name).
// This process is based on the GraphQL spec's CollectFields algorithm but adapted for static analysis
// (i.e., it cannot depend on runtime arguments for @skip/@include, only static true/false literals).
// visitedFragments is used to detect and prevent infinite loops from recursive fragment spreads.
func (t *Typer) collectFieldsStatic(selectionSet ast.SelectionSet, visitedFragments *orderedmap.OrderedMap[string, struct{}]) (*orderedmap.OrderedMap[string, []*SelectedFieldNode], error) {
	groupedFields := orderedmap.NewOrderedMap[string, []*SelectedFieldNode]()

	for _, selection := range selectionSet {
		sDirectives := getSelectionDirectives(selection)
		// Spec deviation 3.a.i: skip only if we always skip (cannot depend on arguments)
		skipDirective := sDirectives.ForName("skip")
		if skipDirective != nil {
			ifArg := skipDirective.Arguments.ForName("if")
			// Check if 'if' argument is a variable or literal true
			if ifArg != nil && ifArg.Value.Kind == ast.BooleanValue && ifArg.Value.Raw == "true" {
				continue
			}
		}

		// Spec deviation 3.b.i: skip only if we always skip (cannot depend on arguments)
		includeDirective := sDirectives.ForName("include")
		if includeDirective != nil {
			ifArg := includeDirective.Arguments.ForName("if")
			// Check if 'if' argument is a variable or literal false
			if ifArg != nil && ifArg.Value.Kind == ast.BooleanValue && ifArg.Value.Raw == "false" {
				continue
			}
		}

		switch s := selection.(type) {
		case *ast.Field:
			responseKey := s.Alias
			if responseKey == "" {
				responseKey = s.Name
			}
			listForResponseKey, _ := groupedFields.Get(responseKey)
			listForResponseKey = append(listForResponseKey, &SelectedFieldNode{SelectedBy: s, Field: s})
			groupedFields.Set(responseKey, listForResponseKey)
		case *ast.FragmentSpread:
			fragmentSpreadName := s.Name
			if visitedFragments.Has(fragmentSpreadName) {
				continue
			}
			visitedFragments.Set(fragmentSpreadName, struct{}{}) // Mark as visited for this path before recursion.

			fragment, ok := t.fragments.Get(fragmentSpreadName)
			if !ok { // Spec deviation 3.d.v: fail instead of continuing
				return nil, gqlerror.Errorf("Referenced Fragment did not exist: %s", fragmentSpreadName)
			}
			// Spec deviation 3.d.vi-vii: fragment may apply to anything here
			// Copy visitedFragments to prevent modification across sibling fragment expansions.
			newVisited := visitedFragments.Copy()
			fragmentGroupedFieldSet, err := t.collectFieldsStatic(fragment.SelectionSet, newVisited)
			if err != nil {
				return nil, err
			}
			for el := fragmentGroupedFieldSet.Front(); el != nil; el = el.Next() {
				responseKey := el.Key
				fragmentGroup := el.Value // fragmentGroup is []*SelectedFieldNode

				listForResponseKey, _ := groupedFields.Get(responseKey)
				for _, sfn := range fragmentGroup {
					listForResponseKey = append(listForResponseKey, &SelectedFieldNode{SelectedBy: s, Field: sfn.Field})
				}
				groupedFields.Set(responseKey, listForResponseKey)
			}
		case *ast.InlineFragment:
			// Spec deviation 3.e.i-ii: fragment may apply to anything here
			// Copy visitedFragments to prevent modification across sibling fragment expansions.
			newVisited := visitedFragments.Copy()
			fragmentGroupedFieldSet, err := t.collectFieldsStatic(s.SelectionSet, newVisited)
			if err != nil {
				return nil, err
			}
			for el := fragmentGroupedFieldSet.Front(); el != nil; el = el.Next() {
				responseKey := el.Key
				fragmentGroup := el.Value // fragmentGroup is []*SelectedFieldNode

				listForResponseKey, _ := groupedFields.Get(responseKey)
				for _, sfn := range fragmentGroup {
					listForResponseKey = append(listForResponseKey, &SelectedFieldNode{SelectedBy: s, Field: sfn.Field})
				}
				groupedFields.Set(responseKey, listForResponseKey)
			}
		default:
			return nil, gqlerror.Errorf("Programmer error: unhandled selection type %T", selection)
		}
	}
	return groupedFields, nil
}

// getBaseTypeName recursively unwraps an ast.Type (which can be a list or non-null wrapper)
// to find the name of its innermost GraphQL named type (e.g., "String", "MyObject").
func getBaseTypeName(typ *ast.Type) string {
	if typ.Elem != nil {
		return getBaseTypeName(typ.Elem)
	}
	return typ.NamedType
}

// collectFieldWireTypes is a core recursive function that determines the Argo wire.Type for a given selectionSet
// against a parentTypeDef. It populates `t.types` with the wire type for each specific ast.Field occurrence.
//
// Parameters:
//   - parentTypeDef: The GraphQL definition of the type (Object, Interface, Union) containing the selectionSet.
//   - selectionSet: The set of fields, fragment spreads, and inline fragments being selected.
//   - getField: A function to resolve a field name to its *ast.FieldDefinition, considering type conditions.
//
// The process involves two main passes over the fields collected by collectFieldsStatic:
//  1. First Pass: Resolve and cache all *ast.FieldDefinition for each selected field. This ensures that
//     field definitions are available before attempting to determine their wire types, especially for
//     complex cases involving fragments and type conditions.
//  2. Second Pass: Construct the wire.Type for each field. If a field has its own sub-selectionSet,
//     collectFieldWireTypes is called recursively. The wire types for fields with the same response key
//     (alias) are then merged according to GraphQL field merging rules.
//
// The function also handles omittability of fields based on whether they are selected directly on the parent
// or via conditional fragments/directives.
func (t *Typer) collectFieldWireTypes(
	parentTypeDef *ast.Definition, // The definition of the type containing the selectionSet
	selectionSet ast.SelectionSet,
	getField func(name string, typeCondition ...string) (*ast.FieldDefinition, error),
) (wire.Type, error) {
	var recordFields []wire.Field
	// exactSelections tracks response keys (aliases/names) that are selected directly on parentTypeDef
	// (i.e., not through a fragment with a different type condition).
	exactSelections := make(map[string]struct{})
	// recordNodes stores ast.Field instances that themselves result in a record type due to having sub-selections.
	// This is used later to update the t.types map for these specific field occurrences.
	recordNodes := []*ast.Field{}

	initialVisitedFragments := orderedmap.NewOrderedMap[string, struct{}]()
	collected, err := t.collectFieldsStatic(selectionSet, initialVisitedFragments)
	if err != nil {
		return nil, err
	}

	fieldDefCache := make(map[*ast.Field]*ast.FieldDefinition) // Cache for resolved field definitions

	// First pass: resolve and cache FieldDefinitions for all selected fields.
	// This iteration order is deterministic because `collected` is an OrderedMap.
	for elCollected := collected.Front(); elCollected != nil; elCollected = elCollected.Next() {
		// alias := elCollected.Key // Response key (alias or field name)
		fieldsInGroup := elCollected.Value // fieldsInGroup is []*SelectedFieldNode for this response key

		for _, selFieldNode := range fieldsInGroup {
			field := selFieldNode.Field // The specific ast.Field from the query
			selectedBy := selFieldNode.SelectedBy

			// Check if already resolved
			if _, exists := fieldDefCache[field]; exists {
				continue
			}

			var typeCondition string
			switch sb := selectedBy.(type) {
			case *ast.FragmentSpread:
				fragDef, ok := t.fragments.Get(sb.Name)
				if !ok { // Should be caught by collectFieldsStatic
					return nil, gqlerror.Errorf("Fragment %s not found during type collection (pass 1)", sb.Name)
				}
				typeCondition = fragDef.TypeCondition
			case *ast.InlineFragment:
				if sb.TypeCondition != "" {
					typeCondition = sb.TypeCondition
				}
			}

			var fDef *ast.FieldDefinition
			var fieldDefErr error
			if typeCondition != "" {
				fDef, fieldDefErr = getField(field.Name, typeCondition)
			} else {
				fDef, fieldDefErr = getField(field.Name)
			}
			if fieldDefErr != nil {
				return nil, fieldDefErr
			}
			fieldDefCache[field] = fDef

			// An exact selection is one not made through a fragment or inline fragment with a differing type condition.
			exactSelection := typeCondition == "" || parentTypeDef.Name == typeCondition
			if exactSelection {
				responseKey := field.Alias
				if responseKey == "" {
					responseKey = field.Name
				}
				exactSelections[responseKey] = struct{}{}
			}
		}
	}

	// Second pass: build wire types using cached field definitions.
	// This iteration ensures that recordFields (wire.Field) are appended in a deterministic order based on `collected`.
	for elCollected := collected.Front(); elCollected != nil; elCollected = elCollected.Next() {
		alias := elCollected.Key
		fieldsInGroup := elCollected.Value // []*SelectedFieldNode for this alias

		if len(fieldsInGroup) == 0 { // Should not happen if collectFieldsStatic works correctly
			continue
		}

		// Determine the overall omittability for this `alias` in the parent record.
		// An alias is omittable if it's not an exact selection on the parent, or if any path
		// selecting it involves a variable @skip/@include directive.
		_, aliasIsExactOnParent := exactSelections[alias]
		finalAliasOmittable := !aliasIsExactOnParent
		if !finalAliasOmittable { // If exact so far, check for variable directives on any selection path.
			for _, selFieldNode := range fieldsInGroup {
				if t.hasVariableIfDirective("include", selFieldNode.Field.Directives) ||
					t.hasVariableIfDirective("skip", selFieldNode.Field.Directives) ||
					t.hasVariableIfDirective("include", getSelectionDirectives(selFieldNode.SelectedBy)) ||
					t.hasVariableIfDirective("skip", getSelectionDirectives(selFieldNode.SelectedBy)) {
					finalAliasOmittable = true
					break
				}
			}
		}

		// Collect all wire.Field contributions for the current alias from different selection paths.
		var intermediateWireFields []wire.Field

		for _, selFieldNode := range fieldsInGroup {
			field := selFieldNode.Field // The ast.Field specific to this selection path (e.g. `name` in `...on User { name }`)
			fDef, fDefExists := fieldDefCache[field]

			if !fDefExists { // Should have been cached in the first pass.
				return nil, gqlerror.Errorf("Internal error: Could not find cached definition for field %s (alias %s)", field.Name, alias)
			}

			var currentPathWireType wire.Type // The wire type for this specific path/selection of the field.
			if len(field.SelectionSet) > 0 {
				// Field has sub-selections, so it will be a record or a list/nullable of records.
				// First, get the base wire type of the field itself (e.g., User, [User], User!, [User]!).
				wrappedFieldWT, err := t.typeToWireType(fDef.Type, fDef.Directives)
				if err != nil {
					return nil, err
				}
				// Unwrap it to get the underlying record type (e.g., User) and the wrapper function.
				unwrappedRes, err := t.unwrapWireType(wrappedFieldWT)
				if err != nil {
					return nil, err
				}

				fieldTypeName := getBaseTypeName(fDef.Type) // e.g., "User"
				fieldParentDefFromSchema := t.schema.Types[fieldTypeName]
				if fieldParentDefFromSchema == nil {
					return nil, gqlerror.Errorf("No definition found for type %s of field %s (alias %s)", fieldTypeName, fDef.Name, alias)
				}

				innerGetField := t.makeGetField(fieldParentDefFromSchema)
				// Recursively call collectFieldWireTypes for the sub-selection.
				innerRecordWireType, err := t.collectFieldWireTypes(fieldParentDefFromSchema, field.SelectionSet, innerGetField)
				if err != nil {
					return nil, err
				}
				// Re-apply the wrappers (List, NonNull) to the derived inner record type.
				currentPathWireType = unwrappedRes.Wrap(innerRecordWireType)
				recordNodes = append(recordNodes, field) // Mark this ast.Field as one that resulted in a record.
			} else {
				// Field is a scalar, enum, or other leaf type.
				currentPathWireType, err = t.typeToWireType(fDef.Type, fDef.Directives)
				if err != nil {
					return nil, err
				}
			}
			// Store the computed wire type for this specific occurrence of the field in the query AST.
			t.types.Set(field.Position.Start, currentPathWireType)

			// Determine if this specific path makes the field contribution omittable for merging.
			// A path is conditionally included if it's part of a fragment with a non-matching type condition
			// or has variable directives.
			pathIsConditionallyIncluded := false
			switch sb := selFieldNode.SelectedBy.(type) {
			case *ast.FragmentSpread:
				fragDef, _ := t.fragments.Get(sb.Name)           // Assumed to exist from earlier checks.
				if fragDef.TypeCondition != parentTypeDef.Name { // If fragment applies to a more specific type.
					pathIsConditionallyIncluded = true
				}
			case *ast.InlineFragment:
				if sb.TypeCondition != "" && sb.TypeCondition != parentTypeDef.Name {
					pathIsConditionallyIncluded = true
				}
			}
			if t.hasVariableIfDirective("include", field.Directives) ||
				t.hasVariableIfDirective("skip", field.Directives) ||
				t.hasVariableIfDirective("include", getSelectionDirectives(selFieldNode.SelectedBy)) ||
				t.hasVariableIfDirective("skip", getSelectionDirectives(selFieldNode.SelectedBy)) {
				pathIsConditionallyIncluded = true
			}

			intermediateWireFields = append(intermediateWireFields, wire.Field{
				Name:      alias,
				Of:        currentPathWireType,
				Omittable: pathIsConditionallyIncluded, // Omittability for this path's contribution.
			})
		}

		if len(intermediateWireFields) == 0 { // Should not occur if fieldsInGroup was non-empty.
			continue
		}

		// Merge all wire types collected for this alias from potentially multiple selection paths.
		mergedFieldForAlias := intermediateWireFields[0]
		if len(intermediateWireFields) > 1 {
			for i := 1; i < len(intermediateWireFields); i++ {
				var errFieldMerge error
				mergedFieldForAlias, errFieldMerge = t.mergeFieldWireType(mergedFieldForAlias, intermediateWireFields[i])
				if errFieldMerge != nil {
					return nil, fmt.Errorf("error merging multiple selections for alias %s: %w", alias, errFieldMerge)
				}
			}
		}

		// The final wire.Field to be added to the parent record.
		// Its name is the alias, its type is the merged type from all paths,
		// and its omittability is `finalAliasOmittable` (based on exactness and variable directives on any path).
		finalOutputField := wire.Field{
			Name:      alias,
			Of:        mergedFieldForAlias.Of,
			Omittable: finalAliasOmittable,
		}
		recordFields = append(recordFields, finalOutputField)
	}

	// The `recordFields` slice now contains one wire.Field per unique alias, with types merged.
	// `groupOverlapping` further refines this by ensuring field order and potentially merging fields if any
	// identical aliases somehow persisted (though the logic above should prevent this).
	// Primarily, it constructs the wire.RecordType from these fields.
	record, err := t.groupOverlapping(recordFields)
	if err != nil {
		return nil, err
	}

	// Update t.types map: For AST field nodes that resulted in records (i.e., had sub-selections),
	// their entry in t.types should point to the parent record type (`record`) they are part of.
	// This specific behavior maps an AST node (e.g., `users` in `{ users { id } }`)
	// to the record type of its parent (the type of the object containing `users`).
	for _, fieldNode := range recordNodes { // fieldNode is an *ast.Field that had sub-selections.
		// The `fieldNode.Position.Start` is the key for the specific AST occurrence.
		// `record` is the wire.RecordType of the parent object containing this fieldNode.
		t.types.Set(fieldNode.Position.Start, record)
	}
	return record, nil
}

// isWireRecord recursively unwraps a wire.Type (handling NullableType, ArrayType, BlockType)
// to determine if the innermost type is a wire.RecordType.
func isWireRecord(wt wire.Type) bool {
	current := wt
	for {
		switch t := current.(type) {
		case wire.NullableType:
			current = t.Of
		case wire.ArrayType:
			current = t.Of
		case wire.BlockType:
			current = t.Of
		case wire.RecordType:
			return true
		default:
			return false
		}
	}
}

// hasVariableIfDirective checks if a given directive list contains a directive (by name, e.g., "skip" or "include")
// that has an "if" argument whose value is a GraphQL variable (e.g., @skip(if: $myVar)).
// This is used to determine if a field's inclusion is dynamic, making it potentially omittable.
func (t *Typer) hasVariableIfDirective(name string, directives ast.DirectiveList) bool {
	if directives == nil {
		return false
	}
	directive := directives.ForName(name)
	if directive == nil {
		return false
	}
	ifArg := directive.Arguments.ForName("if")
	return ifArg != nil && ifArg.Value.Kind == ast.Variable
}

// groupOverlapping takes a slice of wire.Field objects and merges fields that share the same response key (Name).
// The order of fields in the output RecordType is determined by their first appearance in the input `fields` slice.
// This function is crucial for implementing GraphQL's field merging rules, ensuring that multiple selections
// of the same field (possibly through different fragments or paths) result in a single, correctly typed field
// in the final record.
func (t *Typer) groupOverlapping(fields []wire.Field) (wire.RecordType, error) {
	var merged []wire.Field
	// Group fields by name to efficiently find all fields for a given response key.
	groupedByName := util.GroupBy(fields, func(f wire.Field) string { return f.Name })

	// Maintain order based on the first appearance of each field name in the input `fields` slice.
	processedNames := make(map[string]struct{})

	for _, originalField := range fields {
		name := originalField.Name
		if _, ok := processedNames[name]; ok {
			continue // Already processed this name group based on its first appearance.
		}
		processedNames[name] = struct{}{}

		fieldsForName := groupedByName[name]
		if len(fieldsForName) == 0 {
			// This case should ideally not be reached if originalField came from fields.
			continue
		}

		// Start merging with the first field in the group for this name.
		current := fieldsForName[0]

		for i := 1; i < len(fieldsForName); i++ {
			var err error
			current, err = t.mergeFieldWireType(current, fieldsForName[i])
			if err != nil {
				return wire.RecordType{}, fmt.Errorf("error merging fields for response key '%s': %w", name, err)
			}
		}
		merged = append(merged, current)
	}
	return wire.RecordType{Fields: merged}, nil
}

// mergeFieldWireType merges two wire.Field objects, `a` and `b`, which are assumed to have the same Name (response key).
// This function implements the core logic for GraphQL field merging:
//   - If both fields resolve to RecordTypes, their record types are merged recursively.
//   - If one resolves to a RecordType and the other does not, this implies an invalid GraphQL query that should
//     have been caught by validation. The behavior here defaults to using the type of field `a`.
//   - If neither resolves to a RecordType (e.g., they are scalars), their types are assumed to be compatible
//     (as per GraphQL validation), and `a`'s type is used.
//
// The resulting field's Omittable flag is true if either `a` or `b` was omittable.
func (t *Typer) mergeFieldWireType(a, b wire.Field) (wire.Field, error) {
	unwrappedA, err := t.unwrapWireType(a.Of)
	if err != nil {
		return wire.Field{}, fmt.Errorf("cannot unwrap field A ('%s') for merging: %w", a.Name, err)
	}

	var finalType wire.Type
	if recA, okA := unwrappedA.T.(wire.RecordType); okA {
		// Field 'a' is a record (or list/nullable of records).
		unwrappedB, err := t.unwrapWireType(b.Of)
		if err != nil {
			return wire.Field{}, fmt.Errorf("cannot unwrap field B ('%s') for merging with record: %w", b.Name, err)
		}
		if recB, okB := unwrappedB.T.(wire.RecordType); okB {
			// Both are records, merge them.
			mergedRecord, err := t.mergeRecordWireType(recA, recB)
			if err != nil {
				return wire.Field{}, fmt.Errorf("cannot merge record types for field '%s': %w", a.Name, err)
			}
			finalType = unwrappedA.Wrap(mergedRecord) // Re-apply wrappers of A to the merged record.
		} else {
			// 'a' is a record, but 'b' is not. This indicates a GraphQL validation error.
			// Defaulting to A's type as a fallback, but this scenario should ideally not occur.
			// log.Printf("Warning: Merging record field '%s' with non-record field. GraphQL validation might have missed this.", a.Name)
			finalType = a.Of
		}
	} else {
		// Field 'a' is not a record. GraphQL validation ensures that if fields have the same response key,
		// their types must be compatible. For non-record types, this typically means they are identical.
		// We assume a.Of is the correct representative type.
		// TODO: Add a check: if b.Of (unwrapped) is also a record, then this is a conflict (A is scalar, B is record).
		finalType = a.Of
	}

	return wire.Field{Name: a.Name, Of: finalType, Omittable: a.Omittable || b.Omittable}, nil
}

// mergeRecordWireType merges two wire.RecordType objects, `a` and `b`.
// The fields from `a` are prioritized in the merged list. Fields also present in `b` are merged.
// Fields unique to `a` or `b` are included but marked as omittable in the merged type,
// reflecting that they might not be present depending on the specific selection path chosen at runtime.
func (t *Typer) mergeRecordWireType(a, b wire.RecordType) (wire.RecordType, error) {
	var mergedFields []wire.Field
	fieldsInA := make(map[string]wire.Field, len(a.Fields))
	for _, f := range a.Fields {
		fieldsInA[f.Name] = f
	}
	fieldsInB := make(map[string]wire.Field, len(b.Fields))
	for _, f := range b.Fields {
		fieldsInB[f.Name] = f
	}

	processedNames := make(map[string]struct{}) // Tracks field names already added to mergedFields.

	// Iterate through a's fields first to maintain its order preference.
	for _, fieldFromA := range a.Fields {
		name := fieldFromA.Name
		processedNames[name] = struct{}{}

		fieldFromB, bHasField := fieldsInB[name]
		if bHasField {
			// Field exists in both records, merge them.
			mergedField, err := t.mergeFieldWireType(fieldFromA, fieldFromB)
			if err != nil {
				return wire.RecordType{}, fmt.Errorf("error merging field '%s' within records: %w", name, err)
			}
			mergedFields = append(mergedFields, mergedField)
		} else {
			// Field only in A. It must be omittable in the merged type because it's not in B.
			mergedFields = append(mergedFields, wire.Field{Name: fieldFromA.Name, Of: fieldFromA.Of, Omittable: true})
		}
	}

	// Add fields from B that were not in A (i.e., not processed yet).
	// These must also be omittable as they are not in A.
	for _, fieldFromB := range b.Fields {
		name := fieldFromB.Name
		if _, ok := processedNames[name]; ok {
			continue // Already processed (was in A).
		}
		mergedFields = append(mergedFields, wire.Field{Name: fieldFromB.Name, Of: fieldFromB.Of, Omittable: true})
	}

	return wire.RecordType{Fields: mergedFields}, nil
}

// typeToWireType converts a GraphQL type usage (*ast.Type, e.g., `[String!]!`) into its corresponding
// Argo wire.Type. It considers GraphQL type kinds (Scalar, Enum, Object, etc.), List and NonNull wrappers,
// and Argo-specific directives like @ArgoCodec and @ArgoDeduplicate found on the type definition.
// `fieldDefDirectives` are directives on the field *using* the type (e.g. @deprecated), not used here
// for codec determination, which comes from the type definition itself.
func (t *Typer) typeToWireType(typeUsage *ast.Type, fieldDefDirectives ast.DirectiveList) (wire.Type, error) {
	if typeUsage.Elem != nil { // Indicates a List type, potentially wrapped by NonNull.
		// Recursively determine the wire type of the list element.
		ofType, err := t.typeToWireType(typeUsage.Elem, fieldDefDirectives) // fieldDefDirectives are passed down but not used for element type codec.
		if err != nil {
			return nil, err
		}
		wt := wire.ArrayType{Of: ofType}
		if typeUsage.NonNull { // If the list itself is NonNull (e.g., [String]!).
			return wt, nil
		}
		return wire.NewNullableType(wt), nil // List is nullable (e.g., [String]).
	}

	// Base case: not a list, so it's a named type (e.g., String, MyObject).
	typeDef := t.schema.Types[typeUsage.NamedType]
	if typeDef == nil {
		return nil, gqlerror.Errorf("Type %s not found in schema", typeUsage.NamedType)
	}

	var baseWireType wire.Type // The wire type for the named type itself, before considering Nullable.

	switch typeDef.Kind {
	case ast.Scalar, ast.Enum:
		// For scalars and enums, determine the Argo codec based on @ArgoCodec directive or defaults.
		codecArgs, err := argoDirs.GetArgoCodecArgs(typeDef.Directives) // Directives on the type definition (e.g., scalar String @ArgoCodec(...)).
		if err != nil {
			return nil, fmt.Errorf("failed to parse @ArgoCodec on type %s: %w", typeDef.Name, err)
		}
		deduplicateVal, deduplicateIsPresent, err := argoDirs.GetArgoDeduplicateValue(typeDef.Directives)
		if err != nil {
			return nil, fmt.Errorf("failed to parse @ArgoDeduplicate on type %s: %w", typeDef.Name, err)
		}

		var chosenCodecType wire.Type
		if codecArgs.Codec != nil {
			// @ArgoCodec is present, use its specified codec.
			switch *codecArgs.Codec {
			case argoDirs.ArgoCodecValueString:
				chosenCodecType = wire.String
			case argoDirs.ArgoCodecValueInt:
				chosenCodecType = wire.Varint
			case argoDirs.ArgoCodecValueFloat:
				chosenCodecType = wire.Float64
			case argoDirs.ArgoCodecValueBoolean:
				chosenCodecType = wire.Boolean
			case argoDirs.ArgoCodecValueBYTES:
				chosenCodecType = wire.Bytes
			case argoDirs.ArgoCodecValueFIXED:
				if codecArgs.FixedLength == nil {
					return nil, gqlerror.Errorf("@ArgoCodec(codec: FIXED) requires fixedLength argument on %s", typeDef.Name)
				}
				chosenCodecType = wire.FixedType{Length: *codecArgs.FixedLength}
			case argoDirs.ArgoCodecValueDESC:
				chosenCodecType = wire.Desc
			case argoDirs.ArgoCodecValuePATH:
				chosenCodecType = wire.Path
			default:
				return nil, gqlerror.Errorf("Unsupported @ArgoCodec codec value %s on %s", *codecArgs.Codec, typeDef.Name)
			}
		} else {
			if typeDef.Kind == ast.Enum {
				chosenCodecType = wire.String
			} else { // Scalar
				switch typeDef.Name {
				case "String", "ID":
					chosenCodecType = wire.String
				case "Int":
					chosenCodecType = wire.Varint
				case "Float":
					chosenCodecType = wire.Float64
				case "Boolean":
					chosenCodecType = wire.Boolean
				default:
					if typeDef.BuiltIn {
						return nil, gqlerror.Errorf("Unknown built-in scalar %s has no default Argo codec", typeDef.Name)
					}
					return nil, gqlerror.Errorf("Custom scalar %s must have an @ArgoCodec directive", typeDef.Name)
				}
			}
		}

		// Booleans cannot be deduplicated in Argo.
		if chosenCodecType.GetTypeKey() == wire.TypeKeyBoolean && deduplicateIsPresent && deduplicateVal {
			return nil, gqlerror.Errorf("Boolean fields (GraphQL type %s) cannot be deduplicated in Argo", typeDef.Name)
		}

		dedupeSetting := false // Default deduplication to false.
		if deduplicateIsPresent {
			dedupeSetting = deduplicateVal // Use @ArgoDeduplicate value if present.
		} else {
			// If @ArgoDeduplicate is not present, use the default for the chosen codec type.
			dedupeSetting, _ = wire.DeduplicateByDefault(chosenCodecType)
		}

		blockKey := typeDef.Name // Default block key is the GraphQL type name.
		if codecArgs.Key != nil {
			blockKey = *codecArgs.Key // Override with @ArgoCodec(key: "...").
		}

		if chosenCodecType.GetTypeKey() == wire.TypeKeyBoolean {
			// Booleans are special in Argo; they are not block types.
			baseWireType = wire.Boolean
		} else {
			// All other scalars/enums are represented as BlockTypes in Argo.
			// This includes custom scalars mapped to wire.Desc via @ArgoCodec(codec: DESC).
			baseWireType = wire.NewBlockType(chosenCodecType, wire.BlockKey(blockKey), dedupeSetting)
		}

	case ast.Object, ast.Interface, ast.Union:
		// For composite types (Object, Interface, Union), the base wire type is initially an empty RecordType.
		// The actual fields of this record will be determined by `collectFieldWireTypes` based on the query's selection set for this type.
		baseWireType = wire.RecordType{Fields: []wire.Field{}}
	default:
		return nil, gqlerror.Errorf("Unsupported GraphQL type kind %s for type %s encountered during wire type conversion", typeDef.Kind, typeDef.Name)
	}

	// Apply NullableType wrapper if the GraphQL type usage is not NonNull.
	if typeUsage.NonNull {
		return baseWireType, nil
	}
	return wire.NewNullableType(baseWireType), nil
}

// unwrapWireTypeResult holds the result of unwrapping a wire.Type.
// T is the innermost, non-wrapper wire type.
// Wrap is a function that can re-apply the original wrappers to a given wire.Type.
// This is used, for example, to get the underlying RecordType from a List of Nullable Records,
// process the RecordType, and then re-wrap it into a List of Nullable processed Records.
type unwrapWireTypeResult struct {
	T    wire.Type                   // The innermost, non-wrapper type (e.g., RecordType, String, Varint).
	Wrap func(r wire.Type) wire.Type // A function to re-apply the original wrappers (Nullable, Block, Array).
}

// unwrapWireType recursively peels off outer wrapper types (NullableType, BlockType, ArrayType)
// from a given wire.Type until the base, non-wrapper type is reached.
// It returns the base type and a function that can re-apply the stripped wrappers in the correct order.
func (t *Typer) unwrapWireType(wt wire.Type) (unwrapWireTypeResult, error) {
	if nullableWt, ok := wt.(wire.NullableType); ok {
		// It's a NullableType, recurse on its element type.
		res, err := t.unwrapWireType(nullableWt.Of)
		if err != nil {
			return unwrapWireTypeResult{}, err
		}
		// Return the inner result, but augment the Wrap function to add back this NullableType layer.
		return unwrapWireTypeResult{
			T: res.T,
			Wrap: func(r wire.Type) wire.Type {
				return wire.NewNullableType(res.Wrap(r)) // res.Wrap(r) wraps r with inner layers, then we add NewNullableType.
			},
		}, nil
	} else if blockWt, ok := wt.(wire.BlockType); ok {
		// It's a BlockType, recurse.
		res, err := t.unwrapWireType(blockWt.Of)
		if err != nil {
			return unwrapWireTypeResult{}, err
		}
		// Augment Wrap to re-apply this BlockType layer.
		return unwrapWireTypeResult{
			T: res.T,
			Wrap: func(r wire.Type) wire.Type {
				return wire.NewBlockType(res.Wrap(r), blockWt.Key, blockWt.Dedupe)
			},
		}, nil
	} else if arrayWt, ok := wt.(wire.ArrayType); ok {
		// It's an ArrayType, recurse.
		res, err := t.unwrapWireType(arrayWt.Of)
		if err != nil {
			return unwrapWireTypeResult{}, err
		}
		// Augment Wrap to re-apply this ArrayType layer.
		return unwrapWireTypeResult{
			T: res.T,
			Wrap: func(r wire.Type) wire.Type {
				return wire.ArrayType{Of: res.Wrap(r)}
			},
		}, nil
	} else {
		// Base case: wt is not one of the known wrapper types.
		// It's the innermost type. The Wrap function is an identity function at this level.
		return unwrapWireTypeResult{T: wt, Wrap: func(r wire.Type) wire.Type { return r }}, nil
	}
}

// makeGetField returns a closure that can resolve a field name to its *ast.FieldDefinition.
// The closure captures the `parentDef` (the GraphQL type definition on which fields are being selected).
// It allows specifying an optional `typeCondition` to resolve fields on a more specific type
// (e.g., when processing a fragment spread or inline fragment).
func (t *Typer) makeGetField(parentDef *ast.Definition) func(name string, typeCondition ...string) (*ast.FieldDefinition, error) {
	return func(fieldName string, typeCondition ...string) (*ast.FieldDefinition, error) {
		tc := "" // Default to no type condition.
		if len(typeCondition) > 0 {
			tc = typeCondition[0]
		}
		return t.getFieldFromSelection(parentDef, fieldName, tc)
	}
}

// getFieldFromSelection resolves a field name to its *ast.FieldDefinition within the context of a parent GraphQL type.
// It handles meta-fields (__schema, __type, __typename) and fields on Objects, Interfaces.
// If `typeCondition` is provided, it attempts to resolve the field on that specific type, provided it's a valid
// subtype or implementor of `parentDef` (though full validation of this relationship is complex and partly assumed
// to be handled by general GraphQL validation).
//
// Parameters:
//   - parentDef: The *ast.Definition of the type on which the field is being selected.
//   - fieldName: The name of the field to resolve.
//   - typeCondition: An optional GraphQL type name. If provided, resolution is attempted on this type.
func (t *Typer) getFieldFromSelection(parentDef *ast.Definition, fieldName string, typeCondition string) (*ast.FieldDefinition, error) {
	// Handle GraphQL introspection meta-fields first.
	switch fieldName {
	case schemaMetaFieldName:
		// __schema is only available on the root Query type.
		if t.schema.Query != nil && parentDef.Name == t.schema.Query.Name {
			return schemaMetaFieldDef, nil
		}
	case typeMetaFieldName:
		// __type is only available on the root Query type.
		if t.schema.Query != nil && parentDef.Name == t.schema.Query.Name {
			return typeMetaFieldDef, nil
		}
	case typeNameMetaFieldName:
		// __typename is available on any Object, Interface, or Union type.
		switch parentDef.Kind {
		case ast.Object, ast.Interface, ast.Union:
			return typeNameMetaFieldDef, nil
		default:
			// Querying __typename on a scalar/enum is invalid GraphQL.
			return nil, gqlerror.Errorf("__typename cannot be queried on non-composite type %s (kind %s)", parentDef.Name, parentDef.Kind)
		}
	}

	// Determine the effective parent definition to use for field lookup.
	// If a typeCondition is given, we use that; otherwise, the original parentDef.
	currentParentDef := parentDef
	if typeCondition != "" {
		conditionalTypeDef := t.schema.Types[typeCondition]
		if conditionalTypeDef == nil {
			return nil, gqlerror.Errorf("Type condition '%s' not found in schema when looking for field '%s' on parent '%s'", typeCondition, fieldName, parentDef.Name)
		}
		// Note: GraphQL validation should ensure that `typeCondition` is a valid type
		// that could apply in the context of `parentDef` (e.g. an implementing object for an interface parent).
		// We assume this validity here and use `conditionalTypeDef` for the lookup.
		currentParentDef = conditionalTypeDef
	}

	// Resolve field based on the kind of the (potentially conditioned) parent type.
	switch currentParentDef.Kind {
	case ast.Object:
		fd := currentParentDef.Fields.ForName(fieldName)
		if fd == nil {
			return nil, gqlerror.Errorf("Field '%s' not found on Object type '%s'", fieldName, currentParentDef.Name)
		}
		return fd, nil
	case ast.Interface:
		fd := currentParentDef.Fields.ForName(fieldName)
		if fd == nil {
			// Field not directly on the interface. If a more specific typeCondition was used that resolved to this interface,
			// and the field is still not found, it's an error for that specific type path.
			// If no typeCondition was used, or it was the same as parentDef, then field just isn't on interface.
			// The query might rely on fragments on concrete types for this field, but for static definition, it's not on *this* interface.
			errMsg := fmt.Sprintf("Field '%s' not found on Interface type '%s'", fieldName, currentParentDef.Name)
			if typeCondition != "" && typeCondition != parentDef.Name { // Clarify error if a specific type condition was used.
				errMsg = fmt.Sprintf("Field '%s' not found on type '%s' (via type condition on '%s')", fieldName, currentParentDef.Name, parentDef.Name)
			}
			return nil, gqlerror.Errorf("%s", errMsg)
		}
		return fd, nil
	case ast.Union:
		// Fields cannot be selected directly on a Union type. A concrete Object type must be specified
		// via an inline fragment or fragment spread with a type condition.
		// If `currentParentDef` is a Union here, it means no such valid object type condition was applied or resolved.
		return nil, gqlerror.Errorf("Cannot select field '%s' directly on Union type '%s'. Use a fragment spread or inline fragment with a type condition for a specific Object type within the union", fieldName, currentParentDef.Name)
	default:
		// Should not happen for valid GraphQL schemas where fields are selected.
		return nil, gqlerror.Errorf("Cannot get fields from GraphQL type '%s' of kind '%s'", currentParentDef.Name, currentParentDef.Kind)
	}
}
