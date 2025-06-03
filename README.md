# ⛵ argo-go: Argo for Go

`argo-go` is a Go implementation of [Argo](https://msolomon.github.io/argo/),
a compact and compressible binary serialization format for [GraphQL](https://graphql.org/).

It is written in Go and distributed as a Go module.
The MIT-licensed source code is available on GitHub at [github.com/beeper/argo-go](https://github.com/beeper/argo-go).

### Features

| Feature               | Implemented? | Notes                             |
| --------------------- | ------------ | --------------------------------- |
| InlineEverything      | ✅           |                                   |
| SelfDescribing        | ✅           |                                   |
| OutOfBandFieldErrors  | ⚠️           | Inline field errors not supported |
| SelfDescribingErrors  | ⚠️           | Non-self-describing not supported |
| NullTerminatedStrings | ✅           |                                   |
| NoDeduplication       | ✅           |                                   |
| HasUserFlags          | ✅           | No user flags implemented         |

### Dependencies

`argo-go` relies on the following external libraries:

- [github.com/vektah/gqlparser/v2](https://github.com/vektah/gqlparser) for parsing GraphQL schemas and queries
- [github.com/elliotchance/orderedmap/v3](https://github.com/elliotchance/orderedmap) for ordered map implementations, important for maintaining field order as expected in GraphQL.

### Tests

Tests may be run with `go test ./...`

`argo-go` includes a suite of test data adapted from the [reference Argo implementation](https://github.com/msolomon/argo/tree/main/argo-js#readme), facilitating equivalence testing.

### Versioning

`argo-go` uses [Semantic Versioning 2.0.0](https://semver.org/).

### Example

Here's a basic example of how to use `argo-go` to encode a GraphQL response to Argo and decode it back. This example uses a very simple schema and query.

`main/main.go`
```go
package main

import (
	"fmt"
	"github.com/beeper/argo-go"
	"github.com/beeper/argo-go/internal/util"
	"github.com/elliotchance/orderedmap/v3"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// Production code should handle errors, we skip over them for brevity
func main() {
	// 1. Define a simple GraphQL schema
	schemaString := `
    type Query {
      hello: String
      version: Float
    }
  `
	schema := gqlparser.MustLoadSchema(&ast.Source{Name: "example.graphql", Input: schemaString})

	// 2. Define a simple GraphQL query
	queryString := `
    query MyQuery {
      hello
      version
    }
  `
	query := gqlparser.MustLoadQuery(schema, queryString)

	// 3. Create an ExecutionResultCodec
	// The operation name "MyQuery" is optional if there's only one operation in the query document.
	erc, _ := argo.NewExecutionResultCodec(schema, query, "MyQuery")

	// 4. Prepare the data to encode (simulating a GraphQL execution result)
	// The top-level map should conform to the GraphQL ExecutionResult structure, typically with a "data" key.
	dataField := orderedmap.NewOrderedMap[string, any]()
	dataField.Set("hello", "Welcome to Argo!")
	dataField.Set("version", 1.2)

	executionResult := orderedmap.NewOrderedMap[string, any]()
	executionResult.Set("data", dataField)

	fmt.Printf("Original data: %s\n", util.NewOrderedMapJSON[string, any](executionResult).MustMarshalJSON())

	// 5. Encode the data to Argo
	argoBytes, _ := erc.OrderedMapToArgo(executionResult)
	fmt.Printf("Encoded Argo bytes: %x\n", argoBytes.Bytes())

	// 6. Decode the Argo bytes back to an OrderedMap
	decodedResult, _ := erc.ArgoToOrderedMap(argoBytes)
	fmt.Printf("Decoded data:  %s\n", util.NewOrderedMapJSON[string, any](decodedResult).MustMarshalJSON())
}
```

```bash
$ go run main/main.go
Original data: {"data":{"hello":"Welcome to Argo!","version":1.2}}
Encoded Argo bytes: 182057656c636f6d6520746f204172676f2110333333333333f33f0800200003
Decoded data:  {"data":{"hello":"Welcome to Argo!","version":1.2}}
```

This example demonstrates the core round-trip functionality. In a real application, the GraphQL schema and queries would be loaded from files or other sources.
