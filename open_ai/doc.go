package openai

import (
	_ "embed"
	"fmt"
)

//go:embed README.md
var readme string

// handleDoc prints the embedded project README so `llm-proxy doc` can surface
// documentation without shipping a separate runtime file.
func handleDoc(args []string) error {
	fmt.Println(readme)
	return nil
}
