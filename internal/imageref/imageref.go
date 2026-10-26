package imageref

import (
	"fmt"
	"strings"
)

const (
	DockerHub      = "docker.io"
	DefaultTag     = "latest"
	dockerHubAlias = "index.docker.io"
)

type Ref struct {
	Registry   string
	Repository string
	Tag        string
	Digest     string
}

func Parse(raw string) (Ref, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Ref{}, fmt.Errorf("empty image reference")
	}
	if strings.Contains(raw, "${") {
		return Ref{}, fmt.Errorf("unresolved variable in image reference %q", raw)
	}

	ref := Ref{}
	name := raw
	if at := strings.Index(name, "@"); at >= 0 {
		ref.Digest = name[at+1:]
		name = name[:at]
	}
	if colon := strings.LastIndex(name, ":"); colon > strings.LastIndex(name, "/") {
		ref.Tag = name[colon+1:]
		name = name[:colon]
	}
	if name == "" {
		return Ref{}, fmt.Errorf("missing repository in image reference %q", raw)
	}

	ref.Registry, ref.Repository = splitRegistry(name)
	if ref.Tag == "" && ref.Digest == "" {
		ref.Tag = DefaultTag
	}
	return ref, nil
}

func splitRegistry(name string) (string, string) {
	first, rest, found := strings.Cut(name, "/")
	if found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		if first == dockerHubAlias {
			first = DockerHub
		}
		if first == DockerHub && !strings.Contains(rest, "/") {
			rest = "library/" + rest
		}
		return first, rest
	}
	if !found {
		return DockerHub, "library/" + name
	}
	return DockerHub, name
}

// Name is the registry-qualified repository, used as the grouping key across stacks.
func (r Ref) Name() string {
	return r.Registry + "/" + r.Repository
}

// ShortName drops the implicit docker.io and library/ parts, matching how people write images in compose files.
func (r Ref) ShortName() string {
	if r.Registry != DockerHub {
		return r.Name()
	}
	return strings.TrimPrefix(r.Repository, "library/")
}

func (r Ref) String() string {
	s := r.ShortName()
	if r.Tag != "" {
		s += ":" + r.Tag
	}
	if r.Digest != "" {
		s += "@" + r.Digest
	}
	return s
}
