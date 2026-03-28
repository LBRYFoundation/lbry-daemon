package blob

import "crypto/aes"
import "crypto/cipher"
import "encoding/hex"
import "encoding/json"
import "fmt"

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
