package main

import (
	"fmt"
	"os"

	"angulus/internal/release"
)

func main() {
	root, err := release.FindRoot(".")
	if err == nil {
		err = release.New(root).Size()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
