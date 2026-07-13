// Package database contains the legacy SDK daemon persistence schema,
// revision migration chain, and runtime storage boundaries.
package database

import (
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	// CurrentRevision is the database revision expected by the pinned Python SDK.
	CurrentRevision = 15

	RevisionFilename = "db_revision"
	SQLiteFilename   = "lbrynet.sqlite"
)

// ErrMigrationRequired is returned when an older revision is found without a
// migration callback. The revision file is left unchanged.
var ErrMigrationRequired = errors.New("database migration is required")

// MigrationFunc upgrades the database from one revision to another. The
// revision file is rewritten only when the callback returns nil.
type MigrationFunc func(fromRevision, toRevision int) error

// RevisionResult describes a successful revision check.
type RevisionResult struct {
	PreviousRevision int
	CurrentRevision  int
	Created          bool
	Migrated         bool
}

// InvalidRevisionError mirrors Python's failure to parse the stripped file as
// a base-10 integer.
type InvalidRevisionError struct {
	Value string
}

func (err *InvalidRevisionError) Error() string {
	return fmt.Sprintf("invalid literal for int() with base 10: %s", pythonStringRepr(err.Value))
}

// IncompatibleRevisionError is returned when the on-disk database is newer
// than this daemon.
type IncompatibleRevisionError struct {
	DatabaseRevision string
	ExpectedRevision int
}

func (err *IncompatibleRevisionError) Error() string {
	return fmt.Sprintf(
		"This version of lbrynet is not compatible with the database\nYour database is revision %s, expected %d",
		err.DatabaseRevision,
		err.ExpectedRevision,
	)
}

// RevisionPath returns the legacy db_revision path for dataDir.
func RevisionPath(dataDir string) string {
	return filepath.Join(dataDir, RevisionFilename)
}

// SQLitePath returns the legacy lbrynet.sqlite path for dataDir.
func SQLitePath(dataDir string) string {
	return filepath.Join(dataDir, SQLiteFilename)
}

// EnsureRevision applies the legacy DatabaseComponent revision-file lifecycle.
// It deliberately does not create dataDir: the Python component expects its
// caller to have prepared the directory before startup.
func EnsureRevision(dataDir string, migrate MigrationFunc) (RevisionResult, error) {
	result := RevisionResult{CurrentRevision: CurrentRevision}
	path := RevisionPath(dataDir)

	if _, err := os.Stat(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return result, fmt.Errorf("stat database revision: %w", err)
		}
		if err := writeRevision(path, CurrentRevision); err != nil {
			return result, err
		}
		result.Created = true
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		return result, fmt.Errorf("read database revision: %w", err)
	}
	value := strings.TrimFunc(string(contents), pythonWhitespace)
	revision, err := parsePythonDecimal(value)
	if err != nil {
		return result, &InvalidRevisionError{Value: value}
	}

	current := big.NewInt(CurrentRevision)
	switch revision.Cmp(current) {
	case 1:
		return result, &IncompatibleRevisionError{
			DatabaseRevision: revision.String(),
			ExpectedRevision: CurrentRevision,
		}
	case 0:
		result.PreviousRevision = CurrentRevision
		return result, nil
	}

	if !revision.IsInt64() || int64(int(revision.Int64())) != revision.Int64() {
		return result, fmt.Errorf("database revision %s cannot be represented by this build", revision)
	}
	result.PreviousRevision = int(revision.Int64())
	if migrate == nil {
		return result, fmt.Errorf(
			"%w: revision %d to %d",
			ErrMigrationRequired,
			result.PreviousRevision,
			CurrentRevision,
		)
	}
	if err := migrate(result.PreviousRevision, CurrentRevision); err != nil {
		return result, err
	}
	if err := writeRevision(path, CurrentRevision); err != nil {
		return result, err
	}
	result.Migrated = true
	return result, nil
}

func writeRevision(path string, revision int) error {
	if err := os.WriteFile(path, []byte(fmt.Sprint(revision)), 0o666); err != nil {
		return fmt.Errorf("write database revision: %w", err)
	}
	return nil
}

func parsePythonDecimal(value string) (*big.Int, error) {
	if value == "" {
		return nil, errors.New("empty integer")
	}

	negative := false
	start := 0
	if value[0] == '+' || value[0] == '-' {
		negative = value[0] == '-'
		start = 1
	}

	digits := make([]byte, 0, len(value))
	previousWasDigit := false
	for _, character := range value[start:] {
		if character == '_' {
			if !previousWasDigit {
				return nil, errors.New("misplaced underscore")
			}
			previousWasDigit = false
			continue
		}
		digit, ok := decimalDigit(character)
		if !ok {
			return nil, errors.New("not a decimal integer")
		}
		digits = append(digits, byte('0'+digit))
		previousWasDigit = true
	}
	if len(digits) == 0 || !previousWasDigit {
		return nil, errors.New("not a decimal integer")
	}

	normalized := string(digits)
	if negative {
		normalized = "-" + normalized
	}
	revision, ok := new(big.Int).SetString(normalized, 10)
	if !ok {
		return nil, errors.New("not a decimal integer")
	}
	return revision, nil
}

func decimalDigit(character rune) (byte, bool) {
	for _, table := range unicode.Nd.R16 {
		if character < 0 || character > 0xffff {
			continue
		}
		value := uint16(character)
		if value < table.Lo || value > table.Hi {
			continue
		}
		offset := value - table.Lo
		if offset%table.Stride == 0 {
			return byte((offset / table.Stride) % 10), true
		}
	}
	for _, table := range unicode.Nd.R32 {
		if character < 0 || character > unicode.MaxRune {
			continue
		}
		value := uint32(character)
		if value < table.Lo || value > table.Hi {
			continue
		}
		offset := value - table.Lo
		if offset%table.Stride == 0 {
			return byte((offset / table.Stride) % 10), true
		}
	}
	return 0, false
}

func pythonWhitespace(character rune) bool {
	return unicode.IsSpace(character) || character >= 0x1c && character <= 0x1f
}

func pythonStringRepr(value string) string {
	var result strings.Builder
	result.WriteByte('\'')
	for _, character := range value {
		switch character {
		case '\\':
			result.WriteString("\\\\")
		case '\'':
			result.WriteString("\\'")
		case '\t':
			result.WriteString("\\t")
		case '\n':
			result.WriteString("\\n")
		case '\r':
			result.WriteString("\\r")
		default:
			if unicode.IsPrint(character) {
				result.WriteRune(character)
			} else if character <= 0xff {
				fmt.Fprintf(&result, "\\x%02x", character)
			} else if character <= 0xffff {
				fmt.Fprintf(&result, "\\u%04x", character)
			} else {
				fmt.Fprintf(&result, "\\U%08x", character)
			}
		}
	}
	result.WriteByte('\'')
	return result.String()
}
