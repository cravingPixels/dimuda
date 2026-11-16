// Package compose finds compose files under a directory tree and extracts the images
// each service uses, resolving ${VAR} references from the stack's .env file.
package compose

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/cravingPixels/dimuda/internal/imageref"
)

const maxDepth = 4

var composeFileNames = map[string]bool{
	"compose.yaml":        true,
	"compose.yml":         true,
	"docker-compose.yaml": true,
	"docker-compose.yml":  true,
}

var variable = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:?[-?])([^}]*))?\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

type Usage struct {
	Source  string
	Stack   string
	Service string
	File    string
	// Raw is the image as written in the file, before ${VAR} interpolation: what an edit must match.
	Raw        string
	Image      string
	Ref        imageref.Ref
	LocalBuild bool
	// Procedure is the Komodo procedure that must deploy this stack, from the
	// dimuda.komodo.procedure label.
	Procedure string
	Err       string
}

type file struct {
	Name     string             `yaml:"name"`
	Services map[string]service `yaml:"services"`
}

type service struct {
	Image  string `yaml:"image"`
	Build  any    `yaml:"build"`
	Labels any    `yaml:"labels"`
}

const procedureLabel = "dimuda.komodo.procedure"

// label reads a label from either compose form: a map, or a list of "key=value".
func (s service) label(key string) string {
	switch labels := s.Labels.(type) {
	case map[string]any:
		if v, ok := labels[key]; ok && v != nil {
			return fmt.Sprint(v)
		}
	case []any:
		for _, item := range labels {
			if k, v, ok := strings.Cut(fmt.Sprint(item), "="); ok && k == key {
				return v
			}
		}
	}
	return ""
}

// Scan walks root and returns one Usage per service that declares an image. Services that
// only have a build section are skipped: there is no registry to check them against.
func Scan(source, root string) ([]Usage, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("compose source %s: %w", source, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("compose source %s: %s is not a directory", source, root)
	}

	var usages []Usage
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || depth(root, path) > maxDepth) {
				return filepath.SkipDir
			}
			return nil
		}
		if !composeFileNames[d.Name()] {
			return nil
		}
		found, err := parseFile(source, root, path)
		if err != nil {
			return err
		}
		usages = append(usages, found...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(usages, func(i, j int) bool {
		if usages[i].Stack != usages[j].Stack {
			return usages[i].Stack < usages[j].Stack
		}
		return usages[i].Service < usages[j].Service
	})
	return usages, nil
}

func depth(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}

func parseFile(source, root, path string) ([]Usage, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(root, path)
	var f file
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return []Usage{{Source: source, Stack: filepath.Base(filepath.Dir(path)), File: rel, Err: fmt.Sprintf("invalid YAML: %v", err)}}, nil
	}

	env := loadDotEnv(filepath.Join(filepath.Dir(path), ".env"))
	stack := f.Name
	if stack == "" || strings.Contains(stack, "$") {
		stack = filepath.Base(filepath.Dir(path))
	}

	var usages []Usage
	for name, svc := range f.Services {
		if svc.Image == "" {
			continue
		}
		u := Usage{Source: source, Stack: stack, Service: name, File: rel, Raw: svc.Image, LocalBuild: svc.Build != nil, Procedure: svc.label(procedureLabel)}
		u.Image = interpolate(svc.Image, env)
		ref, err := imageref.Parse(u.Image)
		if err != nil {
			u.Err = err.Error()
		}
		u.Ref = ref
		usages = append(usages, u)
	}
	return usages, nil
}

// interpolate follows the compose spec: ${VAR}, $VAR, ${VAR:-default} and ${VAR-default}.
// Unknown variables without a default are left in place so the caller can report them.
func interpolate(s string, env map[string]string) string {
	return variable.ReplaceAllStringFunc(s, func(match string) string {
		m := variable.FindStringSubmatch(match)
		name, op, def := m[1], m[2], m[3]
		if name == "" {
			name = m[4]
		}
		value, set := env[name]
		switch op {
		case ":-":
			if value == "" {
				return def
			}
		case "-":
			if !set {
				return def
			}
		}
		if !set {
			return match
		}
		return value
	})
}

func loadDotEnv(path string) map[string]string {
	env := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return env
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			continue
		}
		env[strings.TrimSpace(key)] = cleanValue(value)
	}
	return env
}

func cleanValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
		if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
			return v[1 : end+1]
		}
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}
