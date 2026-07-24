package reflector

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"lbry/daemon/blob"
)

const (
	clientTimeout         = 30 * time.Second
	maxClientResponseSize = 2_000_000
)

// ReflectStream uploads a locally complete stream using reflector protocol v2.
func ReflectStream(ctx context.Context, address string, manager *blob.BlobManager, sdHash string) ([]string, error) {
	if manager == nil {
		return nil, errors.New("reflector: blob manager is nil")
	}
	descriptorData, ok := manager.Get(sdHash)
	if !ok {
		return nil, fmt.Errorf("reflector: descriptor %s is unavailable", sdHash)
	}
	descriptor, err := blob.DecodeDescriptor(sdHash, descriptorData)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: clientTimeout}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	} else {
		_ = connection.SetDeadline(time.Now().Add(clientTimeout))
	}
	reader := bufio.NewReader(connection)
	handshake, err := reflectorResponse(connection, reader, map[string]any{"version": 1})
	if err != nil {
		return nil, err
	}
	if version, ok := handshake["version"].(float64); !ok || int(version) != 1 {
		return nil, errors.New("reflector: incompatible protocol version")
	}

	reflected := make([]string, 0, len(descriptor.ContentBlobs())+1)
	response, err := reflectorResponse(connection, reader, map[string]any{
		"sd_blob_hash": sdHash, "sd_blob_size": len(descriptorData),
	})
	if err != nil {
		return reflected, err
	}
	if send, ok := response["send_sd_blob"].(bool); !ok {
		return reflected, errors.New("reflector: missing send_sd_blob response")
	} else if send {
		if _, err := connection.Write(descriptorData); err != nil {
			return reflected, err
		}
		if err := decodeReceipt(reader, "received_sd_blob"); err != nil {
			return reflected, err
		}
		reflected = append(reflected, sdHash)
	}

	needed := stringList(response["needed_blobs"])
	if len(needed) == 0 && len(reflected) == 1 {
		for _, info := range descriptor.ContentBlobs() {
			needed = append(needed, info.BlobHash)
		}
	}
	for _, hash := range needed {
		data, exists := manager.Get(hash)
		if !exists {
			continue
		}
		blobResponse, requestErr := reflectorResponse(connection, reader, map[string]any{
			"blob_hash": hash, "blob_size": len(data),
		})
		if requestErr != nil {
			return reflected, requestErr
		}
		send, valid := blobResponse["send_blob"].(bool)
		if !valid {
			return reflected, errors.New("reflector: missing send_blob response")
		}
		if !send {
			continue
		}
		if _, err := connection.Write(data); err != nil {
			return reflected, err
		}
		if err := decodeReceipt(reader, "received_blob"); err != nil {
			return reflected, err
		}
		reflected = append(reflected, hash)
	}
	return reflected, nil
}

func reflectorResponse(writer io.Writer, reader *bufio.Reader, request map[string]any) (map[string]any, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(data); err != nil {
		return nil, err
	}
	var response map[string]any
	if err := readJSONMessage(reader, &response, maxClientResponseSize); err != nil {
		return nil, err
	}
	return response, nil
}

func decodeReceipt(reader *bufio.Reader, field string) error {
	var response map[string]any
	if err := readJSONMessage(reader, &response, maxClientResponseSize); err != nil {
		return err
	}
	if received, _ := response[field].(bool); !received {
		return fmt.Errorf("reflector: %s was not acknowledged", field)
	}
	return nil
}

func stringList(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}
