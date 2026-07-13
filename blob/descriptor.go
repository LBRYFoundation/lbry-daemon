package blob

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"unicode/utf8"
)

// StreamDescriptor is the parsed SD blob JSON.
type StreamDescriptor struct {
	StreamName        string     `json:"stream_name"`
	Key               string     `json:"key"` // hex-encoded AES-128 key
	SuggestedFileName string     `json:"suggested_file_name"`
	StreamHash        string     `json:"stream_hash"`
	StreamType        string     `json:"stream_type"`
	Blobs             []BlobInfo `json:"blobs"`
}

type BlobInfo struct {
	BlobHash string `json:"blob_hash,omitempty"`
	BlobNum  int    `json:"blob_num"`
	IV       string `json:"iv"` // hex-encoded 16-byte IV
	Length   int    `json:"length"`
}

// ParseDescriptor parses SD blob JSON bytes into a StreamDescriptor.
func ParseDescriptor(data []byte) (*StreamDescriptor, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("descriptor: empty blob")
	}
	if len(data) > MaxStreamDescriptorSize {
		return nil, fmt.Errorf(
			"descriptor: size %d exceeds the resource limit of %d bytes",
			len(data), MaxStreamDescriptorSize,
		)
	}
	var sd StreamDescriptor
	if err := json.Unmarshal(data, &sd); err != nil {
		return nil, fmt.Errorf("descriptor: parse: %w", err)
	}
	if sd.Key == "" {
		return nil, fmt.Errorf("descriptor: missing key")
	}
	if len(sd.Blobs) == 0 {
		return nil, fmt.Errorf("descriptor: no blobs")
	}
	return &sd, nil
}

// DecodeDescriptor verifies the content-addressed SD blob and applies all
// structural checks before it can be used to allocate or download data blobs.
func DecodeDescriptor(sdHash string, data []byte) (*StreamDescriptor, error) {
	if !canonicalBlobHash(sdHash) {
		return nil, fmt.Errorf("descriptor: invalid sd hash %q", sdHash)
	}
	if len(data) == 0 || len(data) > MaxStreamDescriptorSize {
		_, err := ParseDescriptor(data)
		return nil, err
	}
	if hashBytes(data) != sdHash {
		return nil, fmt.Errorf("descriptor: sd blob hash does not match %s", sdHash[:12])
	}
	descriptor, err := ParseDescriptor(data)
	if err != nil {
		return nil, err
	}
	if err := ValidateDescriptor(descriptor); err != nil {
		return nil, err
	}
	return descriptor, nil
}

// ValidateDescriptor applies the structural and stream-hash checks performed
// by the pinned SDK when an SD blob is loaded.
func ValidateDescriptor(descriptor *StreamDescriptor) error {
	if descriptor == nil || len(descriptor.Blobs) == 0 {
		return fmt.Errorf("descriptor: no blobs")
	}
	if len(descriptor.Blobs) > MaxStreamDescriptorBlobs {
		return fmt.Errorf(
			"descriptor: %d blobs exceeds the resource limit of %d",
			len(descriptor.Blobs), MaxStreamDescriptorBlobs,
		)
	}
	key, err := hex.DecodeString(descriptor.Key)
	if err != nil || len(key) != aes.BlockSize {
		return fmt.Errorf("descriptor: key must be a %d-byte hex value", aes.BlockSize)
	}
	last := len(descriptor.Blobs) - 1
	var total int64
	for index, item := range descriptor.Blobs {
		iv, err := hex.DecodeString(item.IV)
		if err != nil || len(iv) != aes.BlockSize {
			return fmt.Errorf("descriptor: blob %d iv must be a %d-byte hex value", index, aes.BlockSize)
		}
		if item.BlobNum != index {
			return fmt.Errorf("descriptor: stream contains out of order or skipped blobs")
		}
		if index < last && item.Length == 0 {
			return fmt.Errorf("descriptor: contains zero-length data blob")
		}
		if index < last && item.BlobHash == "" {
			return fmt.Errorf("descriptor: data blob %d has no hash", index)
		}
		if index < last && (item.Length < aes.BlockSize || item.Length > MaxBlobSize || item.Length%aes.BlockSize != 0) {
			return fmt.Errorf(
				"descriptor: blob %d length %d must be AES-aligned and between %d and %d bytes",
				index, item.Length, aes.BlockSize, MaxBlobSize,
			)
		}
		if index < last && !canonicalBlobHash(item.BlobHash) {
			return fmt.Errorf("descriptor: data blob %d has an invalid hash", index)
		}
		if index < last {
			plainUpperBound := int64(item.Length - 1)
			if total > math.MaxInt64-plainUpperBound {
				return fmt.Errorf("descriptor: stream size exceeds the resource limit")
			}
			total += plainUpperBound
		}
	}
	terminator := descriptor.Blobs[last]
	if terminator.Length != 0 {
		return fmt.Errorf("descriptor: does not end with a zero-length blob")
	}
	if terminator.BlobHash != "" {
		return fmt.Errorf("descriptor: stream terminator blob should not have a hash")
	}
	encoded, err := MarshalDescriptor(descriptor)
	if err != nil {
		return fmt.Errorf("descriptor: encode resource check: %w", err)
	}
	if len(encoded) > MaxStreamDescriptorSize {
		return fmt.Errorf(
			"descriptor: encoded size %d exceeds the resource limit of %d bytes",
			len(encoded), MaxStreamDescriptorSize,
		)
	}
	normalized := *descriptor
	normalizeMetadata := func(source string) (string, error) {
		decoded, err := hex.DecodeString(source)
		if err != nil || !utf8.Valid(decoded) {
			return "", fmt.Errorf("descriptor: invalid hex-encoded stream metadata")
		}
		return hex.EncodeToString(decoded), nil
	}
	normalized.StreamName, err = normalizeMetadata(descriptor.StreamName)
	if err != nil {
		return err
	}
	normalized.SuggestedFileName, err = normalizeMetadata(descriptor.SuggestedFileName)
	if err != nil {
		return err
	}
	if calculateCreatedStreamHash(&normalized) != descriptor.StreamHash {
		return fmt.Errorf("descriptor: stream hash does not match stream metadata")
	}
	return nil
}

// ContentBlobs returns only the data blobs (excludes the terminator blob with length=0).
func (sd *StreamDescriptor) ContentBlobs() []BlobInfo {
	var blobs []BlobInfo
	for _, b := range sd.Blobs {
		if b.Length > 0 && b.BlobHash != "" {
			blobs = append(blobs, b)
		}
	}
	return blobs
}

// TotalSize returns the total decrypted content size (approximate — last blob may have padding).
func (sd *StreamDescriptor) TotalSize() int64 {
	var total int64
	for _, b := range sd.ContentBlobs() {
		total += int64(b.Length)
	}
	return total
}

// DecryptBlob decrypts a single encrypted blob using AES-128-CBC with PKCS7 padding.
func DecryptBlob(encrypted []byte, keyHex string, ivHex string) ([]byte, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, fmt.Errorf("decrypt: bad key hex: %w", err)
	}
	iv, err := hex.DecodeString(ivHex)
	if err != nil {
		return nil, fmt.Errorf("decrypt: bad iv hex: %w", err)
	}

	if len(key) != 16 {
		return nil, fmt.Errorf("decrypt: key must be 16 bytes, got %d", len(key))
	}
	if len(iv) != aes.BlockSize {
		return nil, fmt.Errorf("decrypt: iv must be %d bytes, got %d", aes.BlockSize, len(iv))
	}
	if len(encrypted) == 0 || len(encrypted)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("decrypt: data length %d not aligned to block size", len(encrypted))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	decrypted := make([]byte, len(encrypted))
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(decrypted, encrypted)

	// Remove PKCS7 padding
	decrypted, err = pkcs7Unpad(decrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	return decrypted, nil
}

func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("pkcs7: empty data")
	}
	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > aes.BlockSize || padLen > len(data) {
		return nil, fmt.Errorf("pkcs7: invalid padding %d", padLen)
	}
	for i := len(data) - padLen; i < len(data); i++ {
		if data[i] != byte(padLen) {
			return nil, fmt.Errorf("pkcs7: inconsistent padding")
		}
	}
	return data[:len(data)-padLen], nil
}
