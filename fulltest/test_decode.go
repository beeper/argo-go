package main

import (
	"fmt"
	"log"
	"os"

	"github.com/beeper/argo-go/codec"
	"github.com/beeper/argo-go/internal/util"
	"github.com/beeper/argo-go/pkg/buf"
	"github.com/beeper/argo-go/wire"
	"github.com/beeper/argo-go/wirecodec"
)

func main() {
	// 1) Read the raw file (here named "types.argo")
	raw, err := os.ReadFile("argo-wire-type-store.argo")
	if err != nil {
		log.Fatalf("read file: %v", err)
	}
	// 4) Decode the store.
	store, err := wirecodec.DecodeWireTypeStoreFile(raw)
	if err != nil {
		log.Fatalf("decode wire-type store: %v", err)
	}

	// 5) Pretty-print.
	for name, t := range store {
		if name == "NewsletterMetadata" {
			fmt.Printf(wire.Print(t))
			raw2, err2 := os.ReadFile("file.argo")
			if err2 != nil {
				log.Fatalf("read file: %v", err)
			}
			decoder, err := codec.NewArgoDecoder(buf.NewBufReadonly(raw2))
			if err != nil {
				return
			}
			data, err3 := decoder.ArgoToMap(t)
			if err3 != nil {
				log.Fatalf("argo to map error: %v", err3)
			}
			fmt.Printf("Decoded data:  %s\n", util.NewOrderedMapJSON[string, any](data).MustMarshalJSON())
		}
	}
}
