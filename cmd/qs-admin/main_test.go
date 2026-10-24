package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapDryRunOfL0File(t *testing.T) {
	var out bytes.Buffer
	if err := bootstrap([]string{"-file", "../../deploy/l0/bootstrap.yaml", "-dry-run"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "region ru-central1 (RU)") || !strings.Contains(out.String(), "nothing created") {
		t.Errorf("plan: %s", out.String())
	}
	if err := bootstrap([]string{"-file", "../../deploy/l0/bootstrap.yaml"}, &out); err == nil {
		t.Error("bootstrap without -dry-run must refuse until the services exist")
	}
}

func TestBootstrapValidation(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"unknown key": "region: {id: r1, country: RU}\nzones: [a]\norganization: {name: o, admin_email: a@b}\nextra: 1\n",
		"bad country": "region: {id: r1, country: Russia}\nzones: [a]\norganization: {name: o, admin_email: a@b}\n",
		"no zones":    "region: {id: r1, country: RU}\norganization: {name: o, admin_email: a@b}\n",
		"bad zone":    "region: {id: r1, country: RU}\nzones: [Zone_A]\norganization: {name: o, admin_email: a@b}\n",
		"no org":      "region: {id: r1, country: RU}\nzones: [a]\n",
	} {
		p := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".yaml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := load(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := bootstrap(nil, &bytes.Buffer{}); err == nil {
		t.Error("missing -file accepted")
	}
}
