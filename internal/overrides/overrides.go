// Package overrides holds per-image knowledge that registries do not expose: where the
// upstream changelog lives (lsio and hotio images point at their wrapper repo) and how to
// read unusual tag formats.
package overrides

import (
	"errors"
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"

	"github.com/cravingPixels/dimuda/internal/imageref"
	"github.com/cravingPixels/dimuda/internal/versioning"
)

type Image struct {
	Changelog string `yaml:"changelog"`
	TagRegex  string `yaml:"tagRegex"`
	Ignore    bool   `yaml:"ignore"`
	Note      string `yaml:"note"`

	parser versioning.Parser
}

func (i Image) Parser() versioning.Parser {
	return i.parser
}

type Set struct {
	images map[string]Image
}

type file struct {
	Images map[string]Image `yaml:"images"`
}

// Load reads path; a missing file yields an empty set so the dashboard works without one.
func Load(path string) (Set, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Set{images: map[string]Image{}}, nil
	}
	if err != nil {
		return Set{}, err
	}
	return Parse(raw)
}

// LoadLayered reads the bundled defaults, which must exist, and then the optional user file,
// whose entries replace the bundled ones image by image.
func LoadLayered(bundled, user string) (Set, error) {
	raw, err := os.ReadFile(bundled)
	if err != nil {
		return Set{}, fmt.Errorf("bundled overrides: %w", err)
	}
	set, err := Parse(raw)
	if err != nil {
		return Set{}, fmt.Errorf("bundled overrides %s: %w", bundled, err)
	}
	custom, err := Load(user)
	if err != nil {
		return Set{}, fmt.Errorf("user overrides %s: %w", user, err)
	}
	for name, img := range custom.images {
		set.images[name] = img
	}
	return set, nil
}

func Parse(raw []byte) (Set, error) {
	var f file
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return Set{}, fmt.Errorf("parse overrides: %w", err)
	}
	set := Set{images: map[string]Image{}}
	for key, img := range f.Images {
		ref, err := imageref.Parse(key)
		if err != nil {
			return Set{}, fmt.Errorf("override %q: %w", key, err)
		}
		if img.parser, err = versioning.NewParser(img.TagRegex); err != nil {
			return Set{}, fmt.Errorf("override %q: %w", key, err)
		}
		set.images[ref.Name()] = img
	}
	return set, nil
}

// For matches by repository, so "redis", "docker.io/redis" and "docker.io/library/redis" are the same key.
func (s Set) For(ref imageref.Ref) Image {
	return s.images[ref.Name()]
}
