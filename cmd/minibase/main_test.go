package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func runCLI(args []string, env map[string]string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, func(k string) string { return env[k] }, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestVersionAndModules(t *testing.T) {
	t.Parallel()

	if code, out, _ := runCLI([]string{"version"}, nil); code != 0 || strings.TrimSpace(out) != version {
		t.Fatalf("version: %d %q", code, out)
	}
	if code, out, _ := runCLI([]string{"modules"}, nil); code != 0 || !strings.Contains(out, "example") {
		t.Fatalf("modules: %d %q", code, out)
	}
}

func TestUnknownCommand(t *testing.T) {
	t.Parallel()

	if code, _, errOut := runCLI([]string{"bogus"}, nil); code != 2 || !strings.Contains(errOut, "Usage") {
		t.Fatalf("got %d %q", code, errOut)
	}
}

func TestServeFailsFastOnBadConfig(t *testing.T) {
	t.Parallel()

	code, _, errOut := runCLI([]string{"serve"}, map[string]string{})
	if code != 1 || !strings.Contains(errOut, "DATABASE_URL is required") {
		t.Fatalf("got %d %q", code, errOut)
	}
}

func TestServeRejectsUnknownModuleBeforeConnecting(t *testing.T) {
	t.Parallel()

	code, _, errOut := runCLI([]string{"serve", "--modules=nope"},
		map[string]string{"MINIBASE_DATABASE_URL": "postgres://unused"})
	if code != 1 || !strings.Contains(errOut, `unknown module "nope"`) {
		t.Fatalf("got %d %q", code, errOut)
	}
}
