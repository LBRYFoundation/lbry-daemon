package peer

import "encoding/json"
import "fmt"
import "io"
import "net"
import "time"

func StartServer(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Println("Error accepting:", err)
			continue
		}
		go handleConnection(conn)
	}
}

func handleConnection(conn net.Conn) {
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
			responseData["available_blobs"] = getAvailableBlobs(requestedBlobsValue.([]string))
		}

		blobDataPaymentRateValue, hasBlobDataPaymentRate := data["blob_data_payment_rate"]
		if hasBlobDataPaymentRate {
			responseData["blob_data_payment_rate"] = getBlobDataPaymentRate(blobDataPaymentRateValue.(float64))
		}

		var incomingBlob map[string]any
		var blobData []byte

		requestedBlobValue, hasRequestedBlob := data["requested_blob"]
		if hasRequestedBlob {
			incomingBlob, blobData = getRequestedBlob(requestedBlobValue.(string))
			responseData["incoming_blob"] = incomingBlob
		}

		jsonEncoder.Encode(responseData)
		if blobData != nil {
			conn.Write(blobData)
		}
	}
}

func getAvailableBlobs(requestedBlobs []string) []string {
	// TODO
	return []string{}
}

func getBlobDataPaymentRate(blobDataPaymentRate float64) string {
	// TODO
	return "RATE_UNSET"
}

func getRequestedBlob(requestedBlob string) (map[string]any, []byte) {
	// TODO
	return map[string]any{}, []byte{}
}
