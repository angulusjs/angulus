package release

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const Registry = "https://registry.npmjs.org"

type Version struct {
	Version    string `json:"version"`
	Prerelease bool   `json:"prerelease"`
	DistTag    string `json:"distTag"`
}

type Artifacts struct {
	Version
	Packages []Artifact `json:"packages"`
}

var tagPattern = regexp.MustCompile(`^v((0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-([0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*))?)$`)
var digits = regexp.MustCompile(`^[0-9]+$`)

func ReleaseVersion(tag string) (Version, error) {
	m := tagPattern.FindStringSubmatch(tag)
	if m != nil {
		for _, part := range strings.Split(m[6], ".") {
			if digits.MatchString(part) && len(part) > 1 && part[0] == '0' {
				m = nil
				break
			}
		}
	}
	if m == nil {
		return Version{}, fmt.Errorf("Release tag must be vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-prerelease (no leading zeroes or build metadata)")
	}
	result := Version{Version: m[1], Prerelease: m[6] != "", DistTag: "latest"}
	if result.Prerelease {
		result.DistTag = "next"
	}
	return result, nil
}

func PackageNames() []string {
	var names []string
	for _, target := range Targets {
		names = append(names, "@angulus/compiler-"+target.Platform+"-"+target.Arch)
	}
	for _, name := range WorkspacePackages {
		names = append(names, "@angulus/"+name)
	}
	return names
}

func ValidateArtifacts(file, tag string) (Artifacts, error) {
	version, err := ReleaseVersion(tag)
	if err != nil {
		return Artifacts{}, err
	}
	var manifest Manifest
	if err := readJSON(file, &manifest); err != nil {
		return Artifacts{}, err
	}
	if manifest.Version != version.Version || manifest.Packages == nil {
		return Artifacts{}, fmt.Errorf("release tag and packed manifest version must match")
	}
	names := PackageNames()
	expected := map[string]bool{}
	for _, name := range names {
		expected[name] = true
	}
	items := map[string]Artifact{}
	for _, item := range manifest.Packages {
		if !expected[item.Name] || items[item.Name].Name != "" {
			return Artifacts{}, fmt.Errorf("release must contain exactly seven platform Go CLI packages plus core, router, forms, http and tooling")
		}
		items[item.Name] = item
	}
	if len(items) != len(names) {
		return Artifacts{}, fmt.Errorf("release must contain exactly seven platform Go CLI packages plus core, router, forms, http and tooling")
	}
	root, err := filepath.Abs(filepath.Dir(file))
	if err != nil {
		return Artifacts{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Artifacts{}, err
	}
	result := Artifacts{Version: version}
	seenFiles := map[string]bool{}
	for _, name := range names {
		item := items[name]
		if item.Version != version.Version {
			return Artifacts{}, fmt.Errorf("Version mismatch for %s", name)
		}
		if item.Tarball == "" || filepath.IsAbs(item.Tarball) || filepath.VolumeName(item.Tarball) != "" || !strings.HasSuffix(item.Tarball, ".tgz") {
			return Artifacts{}, fmt.Errorf("Invalid tarball for %s", name)
		}
		path, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(item.Tarball)))
		if err != nil {
			return Artifacts{}, err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return Artifacts{}, fmt.Errorf("Tarball escapes release directory: %s", name)
		}
		if seenFiles[path] {
			return Artifacts{}, fmt.Errorf("duplicate tarball for %s", name)
		}
		seenFiles[path] = true
		bytes, err := os.ReadFile(path)
		if err != nil {
			return Artifacts{}, err
		}
		hash := sha512.Sum512(bytes)
		integrity := "sha512-" + base64.StdEncoding.EncodeToString(hash[:])
		if item.Integrity != integrity {
			return Artifacts{}, fmt.Errorf("Tarball integrity mismatch for %s", name)
		}
		item.Tarball = path
		result.Packages = append(result.Packages, item)
	}
	return result, nil
}

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

func PublicationPlan(ctx context.Context, artifacts Artifacts, bootstrap bool, client HTTPClient) ([]Artifact, error) {
	var plan []Artifact
	for _, item := range artifacts.Packages {
		req, err := http.NewRequestWithContext(ctx, "GET", Registry+"/"+url.PathEscape(item.Name), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.npm.install-v1+json")
		response, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("npm registry preflight failed for %s: %w. No publish attempted", item.Name, err)
		}
		if response.StatusCode == 404 {
			response.Body.Close()
			if !bootstrap {
				return nil, fmt.Errorf("%s does not exist on npm; a maintainer must bootstrap the package and configure its Trusted Publisher", item.Name)
			}
			item.Action = "publish"
			plan = append(plan, item)
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			response.Body.Close()
			return nil, fmt.Errorf("npm registry preflight failed for %s: HTTP %d. No publish attempted", item.Name, response.StatusCode)
		}
		var metadata struct {
			Name     string `json:"name"`
			Versions map[string]struct {
				Dist struct {
					Integrity string `json:"integrity"`
				} `json:"dist"`
			} `json:"versions"`
			DistTags map[string]string `json:"dist-tags"`
		}
		err = json.NewDecoder(response.Body).Decode(&metadata)
		response.Body.Close()
		if err != nil || metadata.Name != item.Name || metadata.Versions == nil {
			return nil, fmt.Errorf("Invalid npm metadata for %s", item.Name)
		}
		if published, ok := metadata.Versions[item.Version]; ok {
			if published.Dist.Integrity != item.Integrity {
				return nil, fmt.Errorf("%s@%s already exists with different bytes; bump the version, npm releases are immutable", item.Name, item.Version)
			}
			item.Action = "skip"
		} else {
			if latest := metadata.DistTags["latest"]; artifacts.DistTag == "latest" && latest != "" {
				current, err := ReleaseVersion("v" + latest)
				if err != nil {
					return nil, err
				}
				if !current.Prerelease && compareStable(artifacts.Version.Version, latest) <= 0 {
					return nil, fmt.Errorf("refusing to move %s's latest tag backwards from %s to %s", item.Name, latest, item.Version)
				}
			}
			item.Action = "publish"
		}
		plan = append(plan, item)
	}
	return plan, nil
}

func compareStable(a, b string) int {
	aa, bb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, _ := new(big.Int).SetString(aa[i], 10)
		y, _ := new(big.Int).SetString(bb[i], 10)
		if n := x.Cmp(y); n != 0 {
			return n
		}
	}
	return 0
}

func (t *Tool) PublishPackages(ctx context.Context, artifacts Artifacts, plan []Artifact, bootstrap bool) error {
	for _, item := range plan {
		if item.Action == "skip" {
			fmt.Fprintf(t.Out, "Already published with identical integrity: %s@%s\n", item.Name, item.Version)
			continue
		}
		if item.Action != "publish" {
			return fmt.Errorf("invalid publication action for %s", item.Name)
		}
		args := []string{"stage", "publish", item.Tarball, "--access", "public", "--tag", artifacts.DistTag, "--ignore-scripts", "--registry", Registry}
		if !bootstrap {
			args = append(args, "--provenance")
		}
		if _, err := t.npm(ctx, t.Root, args, false); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tool) Publish(ctx context.Context, tag string, dryRun, bootstrap bool) error {
	artifacts, err := ValidateArtifacts(filepath.Join(t.Root, ".angulus", "release", "manifest.json"), tag)
	if err != nil {
		return err
	}
	if flag, ok := os.LookupEnv("RELEASE_PRERELEASE"); ok && flag != fmt.Sprint(artifacts.Prerelease) {
		return fmt.Errorf("GitHub release prerelease flag must match the semantic version tag")
	}
	if dryRun {
		return json.NewEncoder(t.Out).Encode(artifacts)
	}
	if bootstrap && os.Getenv("GITHUB_ACTIONS") != "" {
		return fmt.Errorf("initial bootstrap is a manual maintainer action, not a token-based CI fallback")
	}
	if !bootstrap && (os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("GITHUB_REPOSITORY") != "angulusjs/angulus" || os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL") == "" || os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN") == "") {
		return fmt.Errorf("publishing requires the angulusjs/angulus GitHub Actions Trusted Publisher with id-token: write; use --dry-run to validate locally")
	}
	// Preflight every artifact before the first registry write.
	plan, err := PublicationPlan(ctx, artifacts, bootstrap, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		return err
	}
	return t.PublishPackages(ctx, artifacts, plan, bootstrap)
}
