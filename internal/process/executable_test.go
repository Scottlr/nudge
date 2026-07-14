package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolverSelectsFirstSafePATHCandidate(t *testing.T) {
	current := t.TempDir()
	invalidDirectory := t.TempDir()
	safeDirectory := t.TempDir()
	_ = os.Mkdir(filepath.Join(invalidDirectory, executableName(ExecutableGit)), 0o755)
	safe := makeExecutableFixture(t, safeDirectory, executableName(ExecutableGit))

	identity, err := NewExecutableResolver().Resolve(context.Background(), ResolveExecutableRequest{
		Kind:       ExecutableGit,
		SearchPath: strings.Join([]string{invalidDirectory, safeDirectory}, string(os.PathListSeparator)),
		CurrentDir: current,
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if identity.Source != ExecutablePATH || identity.CanonicalPath != safe {
		t.Fatalf("identity = %#v, want PATH candidate %q", identity, safe)
	}
	if len(identity.NativeID) == 0 || identity.SHA256 == ([32]byte{}) {
		t.Fatal("resolver did not record native identity and digest")
	}
}

func TestResolverExcludesRepositoryAndWorkspaceRoots(t *testing.T) {
	current := t.TempDir()
	repository := t.TempDir()
	safeDirectory := t.TempDir()
	repositoryCandidate := makeExecutableFixture(t, repository, executableName(ExecutableGit))
	safeCandidate := makeExecutableFixture(t, safeDirectory, executableName(ExecutableGit))
	resolver := NewExecutableResolver()
	request := ResolveExecutableRequest{
		Kind:            ExecutableGit,
		SearchPath:      strings.Join([]string{repository, safeDirectory}, string(os.PathListSeparator)),
		CurrentDir:      current,
		RepositoryRoots: []string{repository},
	}
	identity, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if identity.CanonicalPath != safeCandidate {
		t.Fatalf("selected excluded candidate %q, want %q", identity.CanonicalPath, safeCandidate)
	}
	request.ConfiguredPath = repositoryCandidate
	request.SearchPath = ""
	if _, err := resolver.Resolve(context.Background(), request); !errors.Is(err, &ExecutableError{Code: ExecutableErrorExcluded}) {
		t.Fatalf("Resolve() configured excluded error = %v", err)
	}
}

func TestResolverRevalidatesReplacementBeforeLaunch(t *testing.T) {
	current := t.TempDir()
	directory := t.TempDir()
	candidate := makeExecutableFixture(t, directory, executableName(ExecutableGit))
	resolver := NewExecutableResolver()
	identity, err := resolver.Resolve(context.Background(), ResolveExecutableRequest{
		Kind:           ExecutableGit,
		ConfiguredPath: candidate,
		CurrentDir:     current,
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if err := os.WriteFile(candidate, []byte("replacement"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.RevalidateForLaunch(context.Background(), identity); !errors.Is(err, ErrExecutableIdentityChanged) {
		t.Fatalf("RevalidateForLaunch() error = %v, want identity changed", err)
	}

	runner, err := NewRunnerWithResolver(resolver)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), Spec{
		Executable:  identity,
		StdoutLimit: 1024,
		StderrLimit: 1024,
	})
	if !errors.Is(err, ErrExecutableIdentityChanged) {
		t.Fatalf("runner stale identity error = %v", err)
	}
}

func TestResolverSkipsWindowsScriptShadow(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows executable extension policy")
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "git.cmd"), []byte("@echo off"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewExecutableResolver().Resolve(context.Background(), ResolveExecutableRequest{
		Kind:       ExecutableGit,
		SearchPath: directory,
		CurrentDir: t.TempDir(),
	})
	if !errors.Is(err, ErrExecutableNotFound) {
		t.Fatalf("Resolve() error = %v, want not found", err)
	}
}

func TestExecutableHealthIsTerminalSafe(t *testing.T) {
	var digest [32]byte
	digest[0] = 1
	health := (ExecutableIdentity{
		Kind:          ExecutableGit,
		Source:        ExecutableConfigured,
		CanonicalPath: "C:\\tools\\git\x1b[31m",
		SHA256:        digest,
	}).Health("git version\x1b[0m")
	if strings.ContainsAny(health.CanonicalPath+health.Version, "\x1b\x00") {
		t.Fatalf("health projection contains terminal control data: %#v", health)
	}
	if len(health.IdentityHashPrefix) != identityHashPrefix {
		t.Fatalf("identity hash prefix length = %d", len(health.IdentityHashPrefix))
	}
	if health.Trusted {
		t.Fatal("incomplete identity reported trusted")
	}
}

func makeExecutableFixture(t *testing.T, directory, name string) string {
	t.Helper()
	data, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalExistingPath(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
