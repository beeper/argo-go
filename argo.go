package argo

import (
	"github.com/elliotchance/orderedmap/v3"

	"github.com/beeper/argo-go/codec"
	"github.com/beeper/argo-go/header"
	"github.com/beeper/argo-go/pkg/buf"
	"github.com/beeper/argo-go/typer"
	"github.com/beeper/argo-go/wire"

	"github.com/vektah/gqlparser/v2/ast"
)

// Package argo provides the high-level API for encoding and decoding
// GraphQL execution results using the Argo binary format.
// It integrates the schema, query, and codec functionalities to offer
// a streamlined way to convert between Go's ordered maps (representing
// GraphQL results) and their Argo byte representations.

// ExecutionResultCodec is the primary entry point for encoding and decoding
// GraphQL ExecutionResult objects to and from the Argo binary format.
// It utilizes a GraphQL schema and query document to determine the wire types
// for encoding and decoding data, managed by an internal Typer instance.
type ExecutionResultCodec struct {
	schema *ast.Schema        // The GraphQL schema used for type resolution.
	query  *ast.QueryDocument // The GraphQL query document being processed.
	typer  *typer.Typer       // The Typer instance responsible for deriving wire types.
}

// NewExecutionResultCodec creates a new ExecutionResultCodec.
// It requires a GraphQL schema and a query document. An optional operationName
// can be provided to specify which operation in the query document to use;
// if not provided, the Typer will attempt to select a default operation.
// Returns an error if the Typer cannot be initialized (e.g., due to an invalid
// schema, query, or operation name).
func NewExecutionResultCodec(schema *ast.Schema, query *ast.QueryDocument, operationName ...string) (*ExecutionResultCodec, error) {
	t, err := typer.NewTyper(schema, query, operationName...)
	if err != nil {
		return nil, err
	}
	return &ExecutionResultCodec{
		schema: schema,
		query:  query,
		typer:  t,
	}, nil
}

// Typer returns the Typer instance used by this ExecutionResultCodec.
// The Typer is responsible for determining the Argo wire types based on the
// GraphQL schema and query.
func (erc *ExecutionResultCodec) Typer() *typer.Typer {
	return erc.typer
}

// OrderedMapToArgo converts an orderedmap.OrderedMap (representing a GraphQL ExecutionResult)
// into Argo-encoded bytes contained in a buf.Buf.
// The `om` parameter holds the GraphQL execution result (data, errors, extensions).
// This method uses a default ArgoEncoder.
func (erc *ExecutionResultCodec) OrderedMapToArgo(om *orderedmap.OrderedMap[string, interface{}]) (*buf.Buf, error) {
	encoder := codec.NewArgoEncoder()
	// Example: To enable specific Argo header flags, you can modify the encoder's header:
	// encoder.Header().SetFlag(header.HeaderInlineEverythingFlag, true)
	// encoder.Header().SetFlag(header.HeaderSelfDescribingFlag, true)
	return erc.OrderedMapToArgoWithEncoder(om, encoder)
}

// OrderedMapToArgoWithEncoder converts an orderedmap.OrderedMap (representing a GraphQL ExecutionResult)
// into Argo-encoded bytes using a provided ArgoEncoder.
// This allows for customization of the encoding process, such as setting specific header flags.
// The `om` parameter holds the GraphQL execution result.
// The encoder is responsible for deterministic serialization of the map contents.
func (erc *ExecutionResultCodec) OrderedMapToArgoWithEncoder(om *orderedmap.OrderedMap[string, interface{}], encoder *codec.ArgoEncoder) (*buf.Buf, error) {
	// This implementation currently sets these flags by default as it does not support
	// in-band field errors or non-self-describing error structures.
	encoder.Header().SetFlag(header.HeaderOutOfBandFieldErrorsFlag, true)
	encoder.Header().SetFlag(header.HeaderSelfDescribingErrorsFlag, true)

	var typ wire.Type
	var err error

	if encoder.Header().GetFlag(header.HeaderSelfDescribingFlag) {
		typ = wire.Desc
	} else {
		typ, err = erc.typer.RootWireType()
		if err != nil {
			return nil, err
		}
	}

	// The encoder.ValueToArgoWithType method expects a standard Go map.
	// The `nativeMap` is populated respecting the order of the input `om` OrderedMap.
	// For deterministic wire output, the `encoder.ValueToArgoWithType` must process
	// `nativeMap` in a deterministic way (e.g., by sorting its keys internally).
	err = encoder.ValueToArgoWithType(om, typ) // Pass the original value (*orderedmap or nil)
	if err != nil {
		return nil, err
	}

	result, err := encoder.GetResult()
	if err == nil {
		result.SetPosition(0)
	}
	return result, err
}

// ArgoToOrderedMap converts Argo-encoded bytes from a buf.Buf into an orderedmap.OrderedMap.
// This OrderedMap represents a GraphQL ExecutionResult (typically containing "data", "errors", "extensions" keys).
// The method relies on the underlying ArgoDecoder to correctly parse the Argo bytes
// according to the schema and query associated with this ExecutionResultCodec.
func (erc *ExecutionResultCodec) ArgoToOrderedMap(argoBytes *buf.Buf) (*orderedmap.OrderedMap[string, interface{}], error) {
	decoder, err := codec.NewArgoDecoder(argoBytes)
	if err != nil {
		return nil, err
	}

	rootType, err := erc.typer.RootWireType()
	if err != nil {
		return nil, err
	}

	decodedMap, err := decoder.ArgoToMap(rootType)
	if err != nil {
		return nil, err
	}

	if decodedMap == nil {
		// If the decoded map from the codec is nil (e.g. if the input argoBytes represented a top-level null),
		// return a new, empty, non-nil OrderedMap to provide a consistent return type.
		return orderedmap.NewOrderedMap[string, interface{}](), nil
	}

	return decodedMap, nil
}
