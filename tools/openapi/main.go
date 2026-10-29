// Command openapi writes the OpenAPI document of the Assay API to a file.
package main

import (
	"fmt"
	"os"

	"github.com/alternayte/assay/internal/app"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: openapi <file>")
		os.Exit(2)
	}
	doc, err := app.OpenAPIDocument()
	if err == nil {
		err = os.WriteFile(os.Args[1], doc, 0o600)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "openapi:", err)
		os.Exit(1)
	}
}
