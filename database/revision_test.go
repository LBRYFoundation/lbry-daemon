package database

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLegacyDatabasePaths(t *testing.T) {
	t.Parallel()

	directory := filepath.Join("root", "data")
	if got, want := RevisionPath(directory), filepath.Join(directory, "db_revision"); got != want {
		t.Fatalf("RevisionPath() = %q, want %q", got, want)
	}
	if got, want := SQLitePath(directory), filepath.Join(directory, "lbrynet.sqlite"); got != want {
		t.Fatalf("SQLitePath() = %q, want %q", got, want)
	}
}

func TestEnsureRevisionCreatesMissingCurrentRevisionWithoutNewline(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	result, err := EnsureRevision(directory, func(_, _ int) error {
		return errors.New("migration must not run")
	})
	if err != nil {
		t.Fatal(err)
	}
	want := RevisionResult{
		PreviousRevision: CurrentRevision,
		CurrentRevision:  CurrentRevision,
		Created:          true,
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
	contents, err := os.ReadFile(RevisionPath(directory))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "15" {
		t.Fatalf("created revision contents = %q, want %q", contents, "15")
	}
}

func TestEnsureRevisionDoesNotCreateDataDirectory(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "missing", "data")
	_, err := EnsureRevision(directory, nil)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want os.ErrNotExist", err)
	}
	if _, statErr := os.Stat(directory); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("data directory was created: %v", statErr)
	}
}

func TestEnsureRevisionAcceptsPythonIntegerFormsWithoutRewritingCurrentFile(t *testing.T) {
	t.Parallel()

	tests := []string{
		"15",
		"  +0015\n",
		"1_5",
		"\u0661\u0665",
		"\uff11\uff15",
		"\x1c15\x1f",
	}
	for _, original := range tests {
		original := original
		t.Run(original, func(t *testing.T) {
			directory := t.TempDir()
			writeFixtureRevision(t, directory, original)
			result, err := EnsureRevision(directory, func(_, _ int) error {
				return errors.New("migration must not run")
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.PreviousRevision != CurrentRevision || result.Created || result.Migrated {
				t.Fatalf("unexpected result: %#v", result)
			}
			if got := readFixtureRevision(t, directory); got != original {
				t.Fatalf("current revision file was rewritten: got %q, want %q", got, original)
			}
		})
	}
}

func TestEnsureRevisionMigratesOlderRevisionThenWritesCanonicalCurrentRevision(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	const original = "  1_4\n"
	writeFixtureRevision(t, directory, original)
	var calls [][2]int
	result, err := EnsureRevision(directory, func(fromRevision, toRevision int) error {
		calls = append(calls, [2]int{fromRevision, toRevision})
		if got := readFixtureRevision(t, directory); got != original {
			t.Fatalf("revision changed before migration: got %q, want %q", got, original)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := [][2]int{{14, 15}}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("migration calls = %v, want %v", calls, want)
	}
	want := RevisionResult{
		PreviousRevision: 14,
		CurrentRevision:  CurrentRevision,
		Migrated:         true,
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
	if got := readFixtureRevision(t, directory); got != "15" {
		t.Fatalf("migrated revision contents = %q, want %q", got, "15")
	}
}

func TestEnsureRevisionRequiresMigratorForOlderRevision(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeFixtureRevision(t, directory, "14")
	result, err := EnsureRevision(directory, nil)
	if !errors.Is(err, ErrMigrationRequired) {
		t.Fatalf("error = %v, want ErrMigrationRequired", err)
	}
	if result.PreviousRevision != 14 || result.Migrated {
		t.Fatalf("unexpected result: %#v", result)
	}
	if got := readFixtureRevision(t, directory); got != "14" {
		t.Fatalf("revision changed without migrator: %q", got)
	}
}

func TestEnsureRevisionPropagatesMigrationFailureWithoutRewriting(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeFixtureRevision(t, directory, "14")
	wantErr := errors.New("fixture migration failed")
	result, err := EnsureRevision(directory, func(fromRevision, toRevision int) error {
		if fromRevision != 14 || toRevision != 15 {
			t.Fatalf("migration range = %d to %d", fromRevision, toRevision)
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if result.Migrated {
		t.Fatalf("failed migration reported success: %#v", result)
	}
	if got := readFixtureRevision(t, directory); got != "14" {
		t.Fatalf("revision changed after migration failure: %q", got)
	}
}

func TestEnsureRevisionRecreatesFileRemovedBySuccessfulMigration(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := RevisionPath(directory)
	writeFixtureRevision(t, directory, "14")
	result, err := EnsureRevision(directory, func(_, _ int) error {
		return os.Remove(path)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Migrated || readFixtureRevision(t, directory) != "15" {
		t.Fatalf("unexpected successful migration result: %#v", result)
	}
}

func TestEnsureRevisionLeavesCallbackFileEffectsOnMigrationFailure(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := RevisionPath(directory)
	writeFixtureRevision(t, directory, "14")
	wantErr := errors.New("fixture migration failed")
	_, err := EnsureRevision(directory, func(_, _ int) error {
		if removeErr := os.Remove(path); removeErr != nil {
			t.Fatal(removeErr)
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("revision file was unexpectedly recreated: %v", statErr)
	}
}

func TestEnsureRevisionReportsFinalWriteFailureAfterMigration(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := RevisionPath(directory)
	writeFixtureRevision(t, directory, "14")
	result, err := EnsureRevision(directory, func(_, _ int) error {
		if removeErr := os.Remove(path); removeErr != nil {
			return removeErr
		}
		return os.Mkdir(path, 0o755)
	})
	if err == nil || !strings.Contains(err.Error(), "write database revision") {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Migrated {
		t.Fatalf("write failure reported migration complete: %#v", result)
	}
}

func TestEnsureRevisionRejectsNewerRevisionWithoutRewriting(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	const original = "  +0016\n"
	writeFixtureRevision(t, directory, original)
	_, err := EnsureRevision(directory, func(_, _ int) error {
		return errors.New("migration must not run")
	})
	var incompatible *IncompatibleRevisionError
	if !errors.As(err, &incompatible) {
		t.Fatalf("error = %v, want IncompatibleRevisionError", err)
	}
	want := "This version of lbrynet is not compatible with the database\n" +
		"Your database is revision 16, expected 15"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
	if got := readFixtureRevision(t, directory); got != original {
		t.Fatalf("newer revision file was rewritten: got %q, want %q", got, original)
	}
}

func TestEnsureRevisionClassifiesArbitrarilyLargeNewerRevision(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	const revision = "999999999999999999999999999999999"
	writeFixtureRevision(t, directory, revision)
	_, err := EnsureRevision(directory, nil)
	var incompatible *IncompatibleRevisionError
	if !errors.As(err, &incompatible) {
		t.Fatalf("error = %v, want IncompatibleRevisionError", err)
	}
	if incompatible.DatabaseRevision != revision {
		t.Fatalf("database revision = %q, want %q", incompatible.DatabaseRevision, revision)
	}
}

func TestEnsureRevisionRejectsInvalidRevisionWithoutRewriting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		value    string
		stripped string
	}{
		{name: "empty", value: "", stripped: ""},
		{name: "whitespace", value: " \t\n", stripped: ""},
		{name: "word", value: "fifteen", stripped: "fifteen"},
		{name: "float", value: "15.0", stripped: "15.0"},
		{name: "leading underscore", value: "_15", stripped: "_15"},
		{name: "trailing underscore", value: "15_", stripped: "15_"},
		{name: "double underscore", value: "1__5", stripped: "1__5"},
		{name: "space after sign", value: "+ 15", stripped: "+ 15"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			writeFixtureRevision(t, directory, test.value)
			called := false
			_, err := EnsureRevision(directory, func(_, _ int) error {
				called = true
				return nil
			})
			var invalid *InvalidRevisionError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %v, want InvalidRevisionError", err)
			}
			if invalid.Value != test.stripped {
				t.Fatalf("invalid value = %q, want %q", invalid.Value, test.stripped)
			}
			if called {
				t.Fatal("migration ran for invalid revision")
			}
			if got := readFixtureRevision(t, directory); got != test.value {
				t.Fatalf("invalid revision file was rewritten: got %q, want %q", got, test.value)
			}
		})
	}
}

func TestEnsureRevisionReadsRevisionPathAsAFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := os.Mkdir(RevisionPath(directory), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := EnsureRevision(directory, nil)
	if err == nil || !strings.Contains(err.Error(), "read database revision") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func writeFixtureRevision(t *testing.T, directory, contents string) {
	t.Helper()
	if err := os.WriteFile(RevisionPath(directory), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFixtureRevision(t *testing.T, directory string) string {
	t.Helper()
	contents, err := os.ReadFile(RevisionPath(directory))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
