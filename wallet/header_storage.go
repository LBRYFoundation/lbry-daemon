package wallet

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
)

const headerStorageCopyBufferSize = 64 * 1024

type headerStorage interface {
	Size() int64
	ReadAt([]byte, int64) (int, error)
	WriteAt([]byte, int64) (int, error)
	Resize(int64) error
	Commit() error
	Close() error
}

type memoryHeaderStorage struct {
	data []byte
}

func newMemoryHeaderStorage() *memoryHeaderStorage {
	return &memoryHeaderStorage{data: []byte{}}
}

func (storage *memoryHeaderStorage) Size() int64 {
	return int64(len(storage.data))
}

func (storage *memoryHeaderStorage) ReadAt(destination []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, fmt.Errorf("negative header storage offset %d", offset)
	}
	if len(destination) == 0 {
		return 0, nil
	}
	if offset >= int64(len(storage.data)) {
		return 0, io.EOF
	}
	written := copy(destination, storage.data[int(offset):])
	if written != len(destination) {
		return written, io.EOF
	}
	return written, nil
}

func (storage *memoryHeaderStorage) WriteAt(source []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, fmt.Errorf("negative header storage offset %d", offset)
	}
	end := offset + int64(len(source))
	if end < offset || end > int64(maxInt()) {
		return 0, fmt.Errorf("header storage size %d exceeds the Go integer range", end)
	}
	if end > storage.Size() {
		if err := storage.Resize(end); err != nil {
			return 0, err
		}
	}
	return copy(storage.data[int(offset):int(end)], source), nil
}

func (storage *memoryHeaderStorage) Resize(size int64) error {
	if size < 0 || size > int64(maxInt()) {
		return fmt.Errorf("invalid in-memory header storage size %d", size)
	}
	current := int64(len(storage.data))
	switch {
	case size < current:
		storage.data = storage.data[:int(size)]
	case size > current:
		storage.data = append(storage.data, make([]byte, int(size-current))...)
	}
	return nil
}

func (*memoryHeaderStorage) Commit() error { return nil }
func (*memoryHeaderStorage) Close() error  { return nil }

// stagedFileHeaderStorage keeps the working BytesIO equivalent in a sparse
// temporary file. The destination is untouched until Commit, so unclosed
// writes and repairs have the same visibility as the Python implementation.
type stagedFileHeaderStorage struct {
	path        string
	working     *os.File
	workingPath string
	size        int64
}

func openStagedFileHeaderStorage(path string) (*stagedFileHeaderStorage, error) {
	var source *os.File
	source, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		source = nil
	} else if err != nil {
		return nil, err
	}
	if source != nil {
		info, statErr := source.Stat()
		if statErr != nil {
			_ = source.Close()
			return nil, statErr
		}
		if info.IsDir() {
			_ = source.Close()
			return nil, fmt.Errorf("header storage path %s is a directory", path)
		}
	}

	working, err := os.CreateTemp("", ".lbry-headers-*")
	if err != nil {
		if source != nil {
			_ = source.Close()
		}
		return nil, err
	}
	storage := &stagedFileHeaderStorage{
		path:        path,
		working:     working,
		workingPath: working.Name(),
	}
	if source == nil {
		return storage, nil
	}

	info, err := source.Stat()
	if err == nil {
		storage.size = info.Size()
		err = copyHeaderStorage(source, working, storage.size, true)
	}
	closeErr := source.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = working.Truncate(storage.size)
	}
	if err != nil {
		_ = storage.Close()
		return nil, err
	}
	return storage, nil
}

func (storage *stagedFileHeaderStorage) Size() int64 { return storage.size }

func (storage *stagedFileHeaderStorage) ReadAt(destination []byte, offset int64) (int, error) {
	if storage == nil || storage.working == nil {
		return 0, errors.New("staged header storage is closed")
	}
	return storage.working.ReadAt(destination, offset)
}

func (storage *stagedFileHeaderStorage) WriteAt(source []byte, offset int64) (int, error) {
	if storage == nil || storage.working == nil {
		return 0, errors.New("staged header storage is closed")
	}
	if offset < 0 {
		return 0, fmt.Errorf("negative header storage offset %d", offset)
	}
	end := offset + int64(len(source))
	if end < offset {
		return 0, errors.New("header storage offset overflow")
	}
	if offset > storage.size {
		if err := storage.Resize(offset); err != nil {
			return 0, err
		}
	}
	if len(source) == 0 {
		return 0, nil
	}
	written, err := storage.working.WriteAt(source, offset)
	storage.size = max(storage.size, offset+int64(written))
	return written, err
}

func (storage *stagedFileHeaderStorage) Resize(size int64) error {
	if storage == nil || storage.working == nil {
		return errors.New("staged header storage is closed")
	}
	if size < 0 {
		return fmt.Errorf("invalid staged header storage size %d", size)
	}
	if err := storage.working.Truncate(size); err != nil {
		return err
	}
	storage.size = size
	return nil
}

func (storage *stagedFileHeaderStorage) Commit() error {
	if storage == nil || storage.working == nil {
		return errors.New("staged header storage is closed")
	}
	_, statErr := os.Stat(storage.path)
	fresh := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !fresh {
		return statErr
	}
	destination, err := os.OpenFile(storage.path, os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		return err
	}
	if fresh {
		err = destination.Truncate(storage.size)
	}
	if err == nil {
		err = copyHeaderStorage(storage.working, destination, storage.size, fresh)
	}
	closeErr := destination.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (storage *stagedFileHeaderStorage) Close() error {
	if storage == nil || storage.working == nil {
		return nil
	}
	closeErr := storage.working.Close()
	storage.working = nil
	removeErr := os.Remove(storage.workingPath)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	return errors.Join(closeErr, removeErr)
}

// copyHeaderStorage copies a bounded logical prefix. For a fresh destination,
// zero chunks can remain sparse after the caller pre-sizes it. Existing files
// must receive zero chunks too, otherwise stale suffix bytes would leak into
// a repaired-then-regrown working chain.
func copyHeaderStorage(source, destination *os.File, size int64, sparse bool) error {
	buffer := make([]byte, headerStorageCopyBufferSize)
	for offset := int64(0); offset < size; {
		length := min(int64(len(buffer)), size-offset)
		chunk := buffer[:int(length)]
		read, err := source.ReadAt(chunk, offset)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if read != len(chunk) {
			return io.ErrUnexpectedEOF
		}
		if !sparse || !allZeroBytes(chunk) {
			written, err := destination.WriteAt(chunk, offset)
			if err != nil {
				return err
			}
			if written != len(chunk) {
				return io.ErrShortWrite
			}
		}
		offset += length
	}
	return nil
}

func allZeroBytes(value []byte) bool {
	return bytes.Count(value, []byte{0}) == len(value)
}

func maxInt() int {
	return int(^uint(0) >> 1)
}
