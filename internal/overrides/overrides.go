// Package overrides holds per-image knowledge that registries do not expose: where the
// upstream changelog lives (lsio and hotio images point at their wrapper repo) and how to
// read unusual tag formats.
package overrides

import (
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
	// AutoUpdate caps what dimuda applies by itself: none, patch (default) or minor.
	AutoUpdate Level `yaml:"autoUpdate"`

	parser versioning.Parser
}

type Level string

const (
	LevelNone  Level = "none"
	LevelPatch Level = "patch"
	LevelMinor Level = "minor"
)

// Allows reports whether an update of this kind may be applied without review. Pins and
// lsio rebuilds count as patches: neither changes the application version.
func (l Level) Allows(kind versioning.Kind) bool {
	switch l {
	case LevelNone:
		return false
	case LevelMinor:
		return kind == versioning.KindMinor || LevelPatch.Allows(kind)
	}
	return kind == versioning.KindPatch || kind == versioning.KindRebuild || kind == versioning.KindPin
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

// Load reads path. A missing file is an error: without it lsio/hotio changelogs and custom
// tag formats silently disappear.
func Load(path string) (Set, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Set{}, fmt.Errorf("overrides: %w", err)
	}
	return Parse(raw)
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
		switch img.AutoUpdate {
		case "":
			img.AutoUpdate = LevelPatch
		case LevelNone, LevelPatch, LevelMinor:
		default:
			return Set{}, fmt.Errorf("override %q: autoUpdate %q must be none, patch or minor", key, img.AutoUpdate)
		}
		set.images[ref.Name()] = img
	}
	return set, nil
}

// For matches by repository, so "redis", "docker.io/redis" and "docker.io/library/redis" are the same key.
func (s Set) For(ref imageref.Ref) Image {
	img, ok := s.images[ref.Name()]
	if !ok {
		img.AutoUpdate = LevelPatch
	}
	return img
}
