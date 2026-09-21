package main

import (
	"fmt"
	"os"

	"angulus/internal/compiler"
)

func main() {
	if err := compiler.Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
