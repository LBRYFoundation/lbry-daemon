package blob

import "fmt"

type BlobManager struct {
	Blobs map[string][]byte
}

func (blobManager *BlobManager) Has(blobHash string) bool {
	_, ok := blobManager.Blobs[blobHash]
	return ok
}

func (blobManager *BlobManager) Get(blobHash string) []byte {
	blobData, ok := blobManager.Blobs[blobHash]
	if ok {
		return blobData
	}
	return nil
}

func (blobManager *BlobManager) Set(blobHash string, blobData []byte, isStreamDescriptor bool) error {
	if isStreamDescriptor {
		// TODO Process SD blob data
		blobManager.Blobs[blobHash] = blobData
		fmt.Printf("SD BLOB (%s) = %+v\n", blobHash, string(blobData))
		return nil
	}
	// TODO Process blob data
	blobManager.Blobs[blobHash] = blobData
	fmt.Printf("BLOB (%s) = %+v\n", blobHash, blobData)
	return nil
}
