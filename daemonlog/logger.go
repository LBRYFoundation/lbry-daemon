package daemonlog

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	logFilename      = "lbrynet.log"
	legacyMaxLogSize = int64(2 * 1024 * 1024)
	legacyLogBackups = 5
)

type Options struct {
	DataDir    string
	Quiet      bool
	NoLogging  bool
	Verbose    []string
	VerboseSet bool
	Console    io.Writer
}

// Controller owns the process-wide standard logger routing installed by
// ConfigureStandardLogger.
type Controller struct {
	previousOutput io.Writer
	file           *RotatingFile
	noLogging      bool
	verbose        []string
	verboseSet     bool
	closeOnce      sync.Once
	closeErr       error
}

// ConfigureStandardLogger applies the legacy CLI's output routing to Go's
// process-wide standard logger. Quiet keeps file logging but suppresses the
// console; NoLogging installs no file or console output.
func ConfigureStandardLogger(options Options) (*Controller, error) {
	controller := &Controller{
		previousOutput: log.Writer(),
		noLogging:      options.NoLogging,
		verbose:        append([]string(nil), options.Verbose...),
		verboseSet:     options.VerboseSet,
	}
	if options.NoLogging {
		log.SetOutput(io.Discard)
		return controller, nil
	}

	dataDir := options.DataDir
	if dataDir == "" {
		dataDir = "."
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	file, err := NewRotatingFile(filepath.Join(dataDir, logFilename), legacyMaxLogSize, legacyLogBackups)
	if err != nil {
		return nil, err
	}
	controller.file = file

	if options.Quiet {
		log.SetOutput(file)
		return controller, nil
	}
	console := options.Console
	if console == nil {
		console = os.Stderr
	}
	log.SetOutput(io.MultiWriter(file, console))
	return controller, nil
}

// DebugEnabled reports the module selection represented by legacy --verbose.
// Callers that emit levelled logs can use this while standard log output keeps
// its existing unlevelled behavior.
func (controller *Controller) DebugEnabled(module string) bool {
	if controller == nil || controller.noLogging || !controller.verboseSet {
		return false
	}
	if len(controller.verbose) == 0 {
		return module == "lbry" || strings.HasPrefix(module, "lbry.")
	}
	for _, selected := range controller.verbose {
		if module == selected || strings.HasPrefix(module, selected+".") {
			return true
		}
	}
	return false
}

func (controller *Controller) Close() error {
	if controller == nil {
		return nil
	}
	controller.closeOnce.Do(func() {
		log.SetOutput(controller.previousOutput)
		if controller.file != nil {
			controller.closeErr = controller.file.Close()
		}
	})
	return controller.closeErr
}

// RotatingFile implements the size and backup policy used by Python's
// RotatingFileHandler.
type RotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	backups  int
	file     *os.File
	size     int64
	closed   bool
}

func NewRotatingFile(path string, maxBytes int64, backups int) (*RotatingFile, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("log maximum size cannot be negative")
	}
	if backups < 0 {
		return nil, fmt.Errorf("log backup count cannot be negative")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o666)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("inspect log file: %w", err)
	}
	return &RotatingFile{
		path: path, maxBytes: maxBytes, backups: backups, file: file, size: info.Size(),
	}, nil
}

func (file *RotatingFile) Write(contents []byte) (int, error) {
	file.mu.Lock()
	defer file.mu.Unlock()
	if file.closed {
		return 0, os.ErrClosed
	}
	if file.maxBytes > 0 && file.backups > 0 && file.size+int64(len(contents)) >= file.maxBytes {
		if err := file.rotateLocked(); err != nil {
			return 0, err
		}
	}
	written, err := file.file.Write(contents)
	file.size += int64(written)
	return written, err
}

func (file *RotatingFile) Close() error {
	file.mu.Lock()
	defer file.mu.Unlock()
	if file.closed {
		return nil
	}
	file.closed = true
	return file.file.Close()
}

func (file *RotatingFile) rotateLocked() error {
	if err := file.file.Close(); err != nil {
		return fmt.Errorf("close log for rotation: %w", err)
	}
	reopen := func() {
		reopened, err := os.OpenFile(file.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o666)
		if err == nil {
			file.file = reopened
			if info, statErr := reopened.Stat(); statErr == nil {
				file.size = info.Size()
			}
		}
	}
	fail := func(err error) error {
		reopen()
		return err
	}

	if file.backups > 0 {
		oldest := backupPath(file.path, file.backups)
		if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
			return fail(fmt.Errorf("remove oldest log backup: %w", err))
		}
		for index := file.backups - 1; index >= 1; index-- {
			source := backupPath(file.path, index)
			destination := backupPath(file.path, index+1)
			if err := os.Rename(source, destination); err != nil && !os.IsNotExist(err) {
				return fail(fmt.Errorf("rotate log backup: %w", err))
			}
		}
		if err := os.Rename(file.path, backupPath(file.path, 1)); err != nil && !os.IsNotExist(err) {
			return fail(fmt.Errorf("rotate active log: %w", err))
		}
	}

	rotated, err := os.OpenFile(file.path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return fail(fmt.Errorf("open rotated log file: %w", err))
	}
	file.file = rotated
	file.size = 0
	return nil
}

func backupPath(path string, index int) string {
	return fmt.Sprintf("%s.%d", path, index)
}
