package reflector

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

	version := -1

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

		versionValue, hasVersion := data["version"]
		if version == -1 {
			if hasVersion {
				version = int(versionValue.(float64))
				jsonEncoder.Encode(map[string]any{
					"version": version,
				})
				continue
			}
			conn.Close()
			return
		}

		blobHashValue, hasBlobHash := data["blob_hash"]
		blobSizeValue, hasBlobSize := data["blob_size"]

		sdBlobHashValue, hasSDBlobHash := data["sd_blob_hash"]
		sdBlobSizeValue, hasSDBlobSize := data["sd_blob_size"]

		if version >= 0 && hasBlobHash && hasBlobSize {
			blobHash := blobHashValue.(string)
			blobSize := int(blobSizeValue.(float64))

			if len(blobHash) != 96 || blobSize <= 0 || blobSize > 2097152 {
				conn.Close()
				return
			}

			jsonEncoder.Encode(map[string]any{
				"send_blob": true, // TODO: Improve response
			})

			blobData := make([]byte, blobSize)
			_, err := io.ReadFull(conn, blobData)

			// TODO Process blob data
			fmt.Printf("BLOB [%d] (%s) = %+v\n", blobSize, blobHash, blobData)

			jsonEncoder.Encode(map[string]any{
				"received_blob": err == nil,
			})
			if err != nil {
				conn.Close()
				return
			}
			continue
		}

		if version >= 1 && hasSDBlobHash && hasSDBlobSize {
			sdBlobHash := sdBlobHashValue.(string)
			sdBlobSize := int(sdBlobSizeValue.(float64))

			if len(sdBlobHash) != 96 || sdBlobSize <= 0 || sdBlobSize > 2097152 {
				conn.Close()
				return
			}

			jsonEncoder.Encode(map[string]any{
				"send_sd_blob": true, // TODO: Improve response
			})

			sdBlobData := make([]byte, sdBlobSize)
			_, err := io.ReadFull(conn, sdBlobData)

			// TODO Process SD blob data
			fmt.Printf("SD BLOB [%d] (%s) = %+v\n", sdBlobSize, sdBlobHash, string(sdBlobData))

			jsonEncoder.Encode(map[string]any{
				"received_sd_blob": err == nil,
			})
			if err != nil {
				conn.Close()
				return
			}
			continue
		}

		conn.Close()
		return
	}
}
