package peer

import "encoding/json"
import "fmt"
import "io"
import "lbry/daemon/blob"
import "net"
import "time"

func StartServer(blobManager blob.BlobManager, listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Println("Error accepting:", err)
			continue
		}
		go handleConnection(conn, blobManager)
	}
}

func handleConnection(conn net.Conn, blobManager blob.BlobManager) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(10 * time.Second)) // Prevent hanging

	jsonDecoder := json.NewDecoder(conn)
	jsonEncoder := json.NewEncoder(conn)

	for {
		var data map[string]any

		err := jsonDecoder.Decode(&data)
		if err != nil {
			if err == io.EOF {
				fmt.Println("Client disconnected")
				return
			}
			continue
		}

		responseData := map[string]any{}

		requestedBlobsValue, hasRequestedBlobs := data["requested_blobs"]
		if hasRequestedBlobs {
			responseData["available_blobs"] = getAvailableBlobs(blobManager, requestedBlobsValue.([]string))
		}

		blobDataPaymentRateValue, hasBlobDataPaymentRate := data["blob_data_payment_rate"]
		if hasBlobDataPaymentRate {
			responseData["blob_data_payment_rate"] = getBlobDataPaymentRate(blobDataPaymentRateValue.(float64))
		}

		var incomingBlob map[string]any
		var blobData []byte

		requestedBlobValue, hasRequestedBlob := data["requested_blob"]
		if hasRequestedBlob {
			incomingBlob, blobData = getRequestedBlob(blobManager, requestedBlobValue.(string))
			responseData["incoming_blob"] = incomingBlob
		}

		jsonEncoder.Encode(responseData)
		if blobData != nil {
			conn.Write(blobData)
		}
	}
}

func getAvailableBlobs(blobManager blob.BlobManager, requestedBlobs []string) []string {
	var availableBlobs []string

	for _, requestedBlob := range requestedBlobs {
		if blobManager.Has(requestedBlob) {
			availableBlobs = append(availableBlobs, requestedBlob)
		}
	}

	return availableBlobs
}

func getBlobDataPaymentRate(blobDataPaymentRate float64) string {
	// TODO
	return "RATE_UNSET"
}

func getRequestedBlob(blobManager blob.BlobManager, requestedBlob string) (map[string]any, []byte) {
	if blobManager.Has(requestedBlob) {
		blobData := blobManager.Get(requestedBlob)
		return map[string]any{
			"blob_hash": requestedBlob,
			"length":    len(blobData),
		}, blobData
	}

	return map[string]any{
		"blob_hash": "",
		"length":    0,
		"error":     "Blob not found",
	}, nil
}
