package blob

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func policyDescriptor(t *testing.T) *CreatedStream {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.bin")
	if err := os.WriteFile(path, []byte("policy"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := CreateStreamDescriptor(path, bytes.NewReader(sequenceBytes(48)))
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestDescriptorResourcePolicy(t *testing.T) {
	created := policyDescriptor(t)
	if decoded, err := DecodeDescriptor(created.SDHash, created.DescriptorBytes); err != nil ||
		decoded.StreamHash != created.Descriptor.StreamHash {
		t.Fatalf("DecodeDescriptor(valid) = %#v, %v", decoded, err)
	}
	if _, err := DecodeDescriptor(strings.Repeat("0", BlobHashLength), created.DescriptorBytes); err == nil {
		t.Fatal("DecodeDescriptor accepted a raw hash mismatch")
	}
	if _, err := ParseDescriptor(make([]byte, MaxStreamDescriptorSize+1)); err == nil {
		t.Fatal("ParseDescriptor accepted an oversized descriptor")
	}

	for name, mutate := range map[string]func(*StreamDescriptor){
		"negative length":  func(sd *StreamDescriptor) { sd.Blobs[0].Length = -16 },
		"short length":     func(sd *StreamDescriptor) { sd.Blobs[0].Length = 1 },
		"unaligned length": func(sd *StreamDescriptor) { sd.Blobs[0].Length = 17 },
		"oversized length": func(sd *StreamDescriptor) { sd.Blobs[0].Length = MaxBlobSize + 16 },
		"invalid blob hash": func(sd *StreamDescriptor) {
			sd.Blobs[0].BlobHash = strings.Repeat("g", BlobHashLength)
		},
		"short key": func(sd *StreamDescriptor) { sd.Key = "00" },
		"short iv":  func(sd *StreamDescriptor) { sd.Blobs[0].IV = "00" },
		"short terminator iv": func(sd *StreamDescriptor) {
			sd.Blobs[len(sd.Blobs)-1].IV = "00"
		},
	} {
		t.Run(name, func(t *testing.T) {
			copyDescriptor := *created.Descriptor
			copyDescriptor.Blobs = append([]BlobInfo(nil), created.Descriptor.Blobs...)
			mutate(&copyDescriptor)
			if err := ValidateDescriptor(&copyDescriptor); err == nil {
				t.Fatal("ValidateDescriptor accepted invalid resource metadata")
			}
		})
	}

	tooMany := *created.Descriptor
	tooMany.Blobs = make([]BlobInfo, MaxStreamDescriptorBlobs+1)
	if err := ValidateDescriptor(&tooMany); err == nil {
		t.Fatal("ValidateDescriptor accepted too many entries")
	}

	tooLarge := *created.Descriptor
	tooLarge.StreamName = strings.Repeat("00", MaxStreamDescriptorSize)
	tooLarge.StreamHash = CalculateStreamHash(&tooLarge)
	if err := ValidateDescriptor(&tooLarge); err == nil {
		t.Fatal("ValidateDescriptor accepted oversized canonical JSON")
	}
}

func TestBlobManagerSetEnforcesResourcePolicyInEveryMode(t *testing.T) {
	persistent := NewPersistentManager(t.TempDir())
	if _, err := persistent.Start(); err != nil {
		t.Fatal(err)
	}
	for name, manager := range map[string]*BlobManager{
		"memory": NewManager(), "persistent": persistent,
	} {
		t.Run(name, func(t *testing.T) {
			valid := []byte("valid")
			if err := manager.Set(hashBytes(valid), valid, false); err != nil {
				t.Fatalf("valid Set failed: %v", err)
			}
			if err := manager.Set(hashBytes(nil), nil, false); err == nil {
				t.Fatal("empty Set succeeded")
			}
			oversized := make([]byte, MaxBlobSize+1)
			if err := manager.Set(hashBytes(oversized), oversized, false); err == nil {
				t.Fatal("oversized Set succeeded")
			}
			if err := manager.Set(strings.Repeat("0", BlobHashLength), valid, false); err == nil {
				t.Fatal("hash-mismatched Set succeeded")
			}
		})
	}
}

func TestIncomingBlobResourcePolicy(t *testing.T) {
	hash := strings.Repeat("1", BlobHashLength)
	if err := validateIncomingBlob(&IncomingBlob{BlobHash: hash, Length: MaxBlobSize}, hash); err != nil {
		t.Fatalf("maximum blob rejected: %v", err)
	}
	for _, incoming := range []*IncomingBlob{
		nil,
		{BlobHash: hash, Length: 0},
		{BlobHash: hash, Length: -1},
		{BlobHash: hash, Length: MaxBlobSize + 1},
		{BlobHash: strings.Repeat("2", BlobHashLength), Length: 1},
	} {
		if err := validateIncomingBlob(incoming, hash); err == nil {
			t.Fatalf("invalid incoming blob accepted: %#v", incoming)
		}
	}
}
