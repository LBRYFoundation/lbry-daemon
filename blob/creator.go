package blob

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var illegalFileName = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]+`)

type CreatedStream struct {
	Descriptor      *StreamDescriptor
	DescriptorBytes []byte
	SDHash          string
	Blobs           map[string][]byte
}

// CreateStreamDescriptor reproduces StreamDescriptor.create_stream. Passing a
// reader makes the key and IV sequence deterministic for differential tests.
func CreateStreamDescriptor(filePath string, random io.Reader) (*CreatedStream, error) {
	if random == nil {
		random = rand.Reader
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	if len(content) == 0 {
		return nil, fmt.Errorf("cannot create stream from empty file")
	}
	dataBlobCount := (len(content) + MaxBlobSize - 2) / (MaxBlobSize - 1)
	if dataBlobCount+1 > MaxStreamDescriptorBlobs {
		return nil, fmt.Errorf(
			"stream requires %d descriptor entries, exceeding the resource limit of %d",
			dataBlobCount+1, MaxStreamDescriptorBlobs,
		)
	}
	key := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(random, key); err != nil {
		return nil, err
	}
	created := &CreatedStream{Blobs: make(map[string][]byte)}
	blobs := make([]BlobInfo, 0, len(content)/(MaxBlobSize-1)+2)
	for offset, blobNum := 0, 0; offset < len(content); blobNum++ {
		end := min(offset+MaxBlobSize-1, len(content))
		iv := make([]byte, aes.BlockSize)
		if _, err := io.ReadFull(random, iv); err != nil {
			return nil, err
		}
		encrypted, err := encryptStreamBlob(key, iv, content[offset:end])
		if err != nil {
			return nil, err
		}
		digest := sha512.Sum384(encrypted)
		hash := hex.EncodeToString(digest[:])
		created.Blobs[hash] = encrypted
		blobs = append(blobs, BlobInfo{
			BlobHash: hash, BlobNum: blobNum, IV: hex.EncodeToString(iv), Length: len(encrypted),
		})
		offset = end
	}
	terminatorIV := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(random, terminatorIV); err != nil {
		return nil, err
	}
	blobs = append(blobs, BlobInfo{BlobNum: len(blobs), IV: hex.EncodeToString(terminatorIV)})
	fileName := filepath.Base(filePath)
	descriptor := &StreamDescriptor{
		StreamName: hex.EncodeToString([]byte(fileName)), Key: hex.EncodeToString(key),
		SuggestedFileName: hex.EncodeToString([]byte(sanitizePublishedFileName(fileName))),
		StreamType:        "lbryfile", Blobs: blobs,
	}
	descriptor.StreamHash = calculateCreatedStreamHash(descriptor)
	if err := ValidateDescriptor(descriptor); err != nil {
		return nil, err
	}
	created.Descriptor = descriptor
	created.DescriptorBytes, err = marshalPythonDescriptor(descriptor)
	if err != nil {
		return nil, err
	}
	sdDigest := sha512.Sum384(created.DescriptorBytes)
	created.SDHash = hex.EncodeToString(sdDigest[:])
	return created, nil
}

func encryptStreamBlob(key, iv, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padding := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := make([]byte, len(plaintext)+padding)
	copy(padded, plaintext)
	for index := len(plaintext); index < len(padded); index++ {
		padded[index] = byte(padding)
	}
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(padded, padded)
	return padded, nil
}

func calculateCreatedStreamHash(descriptor *StreamDescriptor) string {
	hash := sha512.New384()
	hash.Write([]byte(descriptor.StreamName))
	hash.Write([]byte(descriptor.Key))
	hash.Write([]byte(descriptor.SuggestedFileName))
	blobHash := sha512.New384()
	for _, item := range descriptor.Blobs {
		itemHash := sha512.New384()
		if item.Length != 0 {
			itemHash.Write([]byte(item.BlobHash))
		}
		itemHash.Write([]byte(fmt.Sprint(item.BlobNum)))
		itemHash.Write([]byte(item.IV))
		itemHash.Write([]byte(fmt.Sprint(item.Length)))
		blobHash.Write(itemHash.Sum(nil))
	}
	hash.Write(blobHash.Sum(nil))
	return hex.EncodeToString(hash.Sum(nil))
}

// CalculateStreamHash returns the pinned SDK stream hash for descriptor
// metadata and blob entries. The StreamHash field itself is not included.
func CalculateStreamHash(descriptor *StreamDescriptor) string {
	if descriptor == nil {
		return ""
	}
	return calculateCreatedStreamHash(descriptor)
}

func marshalPythonDescriptor(descriptor *StreamDescriptor) ([]byte, error) {
	quoted := func(value string) (string, error) {
		encoded, err := json.Marshal(value)
		return string(encoded), err
	}
	var buffer bytes.Buffer
	buffer.WriteString(`{"blobs": [`)
	for index, item := range descriptor.Blobs {
		if index > 0 {
			buffer.WriteString(", ")
		}
		iv, err := quoted(item.IV)
		if err != nil {
			return nil, err
		}
		if item.BlobHash != "" {
			blobHash, err := quoted(item.BlobHash)
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&buffer, `{"blob_hash": %s, "blob_num": %d, "iv": %s, "length": %d}`,
				blobHash, item.BlobNum, iv, item.Length)
		} else {
			fmt.Fprintf(&buffer, `{"blob_num": %d, "iv": %s, "length": %d}`, item.BlobNum, iv, item.Length)
		}
	}
	fields := []struct{ name, value string }{
		{"key", descriptor.Key}, {"stream_hash", descriptor.StreamHash},
		{"stream_name", descriptor.StreamName}, {"stream_type", descriptor.StreamType},
		{"suggested_file_name", descriptor.SuggestedFileName},
	}
	buffer.WriteByte(']')
	for _, field := range fields {
		value, err := quoted(field.value)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&buffer, `, %q: %s`, field.name, value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// MarshalDescriptor reproduces the pinned SDK's canonical SD JSON encoding.
func MarshalDescriptor(descriptor *StreamDescriptor) ([]byte, error) {
	if descriptor == nil {
		return nil, fmt.Errorf("descriptor is nil")
	}
	return marshalPythonDescriptor(descriptor)
}

// MarshalOldSortDescriptor reproduces the pre-sort_keys descriptor encoding
// accepted by the pinned SDK when rebuilding a missing SD blob from a wallet.
func MarshalOldSortDescriptor(descriptor *StreamDescriptor) ([]byte, error) {
	if descriptor == nil {
		return nil, fmt.Errorf("descriptor is nil")
	}
	quoted := func(value string) (string, error) {
		encoded, err := json.Marshal(value)
		return string(encoded), err
	}
	streamName, err := quoted(descriptor.StreamName)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	fmt.Fprintf(&buffer, `{"stream_name": %s, "blobs": [`, streamName)
	for index, item := range descriptor.Blobs {
		if index > 0 {
			buffer.WriteString(", ")
		}
		iv, err := quoted(item.IV)
		if err != nil {
			return nil, err
		}
		if item.BlobHash == "" {
			fmt.Fprintf(&buffer, `{"length": %d, "blob_num": %d, "iv": %s}`,
				item.Length, item.BlobNum, iv)
		} else {
			blobHash, err := quoted(item.BlobHash)
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&buffer, `{"length": %d, "blob_num": %d, "blob_hash": %s, "iv": %s}`,
				item.Length, item.BlobNum, blobHash, iv)
		}
		if item.BlobHash == "" {
			break
		}
	}
	buffer.WriteByte(']')
	fields := []struct{ name, value string }{
		{"stream_type", descriptor.StreamType}, {"key", descriptor.Key},
		{"suggested_file_name", descriptor.SuggestedFileName}, {"stream_hash", descriptor.StreamHash},
	}
	for _, field := range fields {
		value, err := quoted(field.value)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&buffer, `, %q: %s`, field.name, value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

func sanitizePublishedFileName(name string) string {
	extension := filepath.Ext(name)
	base := strings.TrimSuffix(name, extension)
	base = strings.TrimSpace(illegalFileName.ReplaceAllString(base, ""))
	extension = strings.TrimSpace(illegalFileName.ReplaceAllString(extension, ""))
	if base == "" {
		base = "lbry_download"
	}
	if len(extension) > 1 {
		base += extension
	}
	return base
}
