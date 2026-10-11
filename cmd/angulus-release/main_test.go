package main

import (
	"context"
	"strings"
	"testing"
)

func TestArgumentValidation(t *testing.T) {
	for _, args := range [][]string{
		nil, {"unknown"}, {"pack", "--unknown"}, {"pack", "--dry-run"},
		{"version"}, {"version", "1.2.3", "1.2.4"}, {"publish", "extra"}, {"validate"},
	} {
		if err := execute(context.Background(), args); err == nil {
			t.Fatalf("accepted invalid args: %v", args)
		}
	}
	err := execute(context.Background(), []string{"pack", "--tag", "v999.0.0"})
	if err == nil || !strings.Contains(err.Error(), "does not match package version") {
		t.Fatal(err)
	}
}
