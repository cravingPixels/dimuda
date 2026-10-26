package config

import (
	"sort"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoad(t *testing.T) {
	c, err := Load(env(map[string]string{
		"COMPOSE_DIRS":    "dockge=/stacks/dockge, /stacks/komodo/",
		"GIT_REPOS":       "https://github.com/cravingPixels/docker-compose-stacks.git",
		"SCAN_INTERVAL":   "12h",
		"NTFY_URL":        "https://ntfy.example.com",
		"NTFY_TOPIC":      "updates",
		"PUID":            "3001",
		"PGID":            "3001",
		"STACKS_READ_GID": "3002, 3003",
	}))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range c.ComposeDirs {
		names = append(names, d.Name+"="+d.Path)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "dockge=/stacks/dockge,komodo=/stacks/komodo/" {
		t.Errorf("dirs = %v", names)
	}
	if len(c.GitRepos) != 1 || c.GitRepos[0].Name != "docker-compose-stacks" {
		t.Errorf("repos = %+v", c.GitRepos)
	}
	if c.PUID != 3001 || c.PGID != 3001 || len(c.ExtraGIDs) != 2 || c.ExtraGIDs[1] != 3003 {
		t.Errorf("ids = %d:%d %v", c.PUID, c.PGID, c.ExtraGIDs)
	}
	if c.UserOverrides != "/config/overrides.yaml" {
		t.Errorf("user overrides = %q", c.UserOverrides)
	}
	if c.ScanInterval != 12*time.Hour || c.ListenAddr != ":8080" || c.DataDir != "/data" || !c.NtfyEnabled() {
		t.Errorf("config = %+v", c)
	}
}

func TestLoadFailsFast(t *testing.T) {
	cases := map[string]map[string]string{
		"nothing to scan":   {},
		"bad interval":      {"COMPOSE_DIRS": "/s", "SCAN_INTERVAL": "soon"},
		"too short":         {"COMPOSE_DIRS": "/s", "SCAN_INTERVAL": "1m"},
		"half ntfy":         {"COMPOSE_DIRS": "/s", "NTFY_URL": "https://ntfy.example.com"},
		"duplicate name":    {"COMPOSE_DIRS": "a=/x,a=/y"},
		"empty named value": {"COMPOSE_DIRS": "a="},
		"root uid":          {"COMPOSE_DIRS": "/s", "PUID": "0"},
		"bad gid":           {"COMPOSE_DIRS": "/s", "PGID": "staff"},
		"bad extra gid":     {"COMPOSE_DIRS": "/s", "STACKS_READ_GID": "3002,x"},
	}
	for name, e := range cases {
		if _, err := Load(env(e)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
