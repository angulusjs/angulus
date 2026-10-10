package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"angulus/internal/release"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := execute(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: angulus-release build-cli|build-packages|pack|version|publish|validate|size")
	}
	command := args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	rootArg := flags.String("root", ".", "workspace root")
	tag := flags.String("tag", "", "exact semantic release tag")
	manifest := flags.String("manifest", "", "release manifest path (validate only)")
	dryRun := flags.Bool("dry-run", false, "validate without registry access")
	bootstrap := flags.Bool("bootstrap", false, "manual initial publication")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	allowed := map[string]map[string]bool{
		"build-cli": {"root": true}, "build-packages": {"root": true}, "size": {"root": true},
		"pack": {"root": true, "tag": true}, "version": {"root": true},
		"validate": {"root": true, "tag": true, "manifest": true},
		"publish":  {"root": true, "tag": true, "dry-run": true, "bootstrap": true},
	}
	if allowed[command] == nil {
		return fmt.Errorf("unknown release command: %s", command)
	}
	var invalid string
	flags.Visit(func(f *flag.Flag) {
		if !allowed[command][f.Name] {
			invalid = f.Name
		}
	})
	if invalid != "" {
		return fmt.Errorf("unknown %s argument: --%s", command, invalid)
	}
	if command == "version" {
		if flags.NArg() != 1 {
			return fmt.Errorf("usage: angulus-release version [--root directory] MAJOR.MINOR.PATCH")
		}
	} else if flags.NArg() != 0 {
		return fmt.Errorf("unexpected %s arguments", command)
	}
	if command == "validate" && *manifest != "" {
		artifacts, err := release.ValidateArtifacts(*manifest, *tag)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(artifacts)
	}
	root, err := release.FindRoot(*rootArg)
	if err != nil {
		return err
	}
	tool := release.New(root)
	switch command {
	case "build-cli":
		return tool.BuildCLI(ctx)
	case "build-packages":
		return tool.BuildPackages(ctx)
	case "pack":
		return tool.Pack(ctx, *tag)
	case "version":
		return tool.UpdateVersion(ctx, flags.Arg(0))
	case "publish":
		return tool.Publish(ctx, *tag, *dryRun, *bootstrap)
	case "validate":
		return fmt.Errorf("validate requires --manifest")
	case "size":
		return tool.Size()
	}
	return nil
}
