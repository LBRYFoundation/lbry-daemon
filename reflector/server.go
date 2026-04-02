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

	var blobHash string
	blobSize := -1

	var sdBlobHash string
	sdBlobSize := -1

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

		versionValue, hasVersion := data["version"].(int)
		if version == -1 {
			if hasVersion {
				version = versionValue
				jsonEncoder.Encode(map[string]any{
					"version": version,
				})
				continue
			}
			conn.Close()
			return
		}

		blobHashValue, hasBlobHash := data["blob_hash"].(string)
		blobSizeValue, hasBlobSize := data["blob_size"].(int)

		sdBlobHashValue, hasSDBlobHash := data["sd_blob_hash"].(string)
		sdBlobSizeValue, hasSDBlobSize := data["sd_blob_size"].(int)

		if blobHash == "" && version >= 0 {
			if hasBlobHash && hasBlobSize {
				if len(blobHashValue) != 96 || blobSizeValue <= 0 || blobSizeValue > 2097152 {
					conn.Close()
					return
				}

				blobHash = blobHashValue
				blobSize = blobSizeValue

				jsonEncoder.Encode(map[string]any{
					"send_blob": false, // TODO: Improve response
				})
				continue
			}
		}

		if sdBlobHash == "" && version >= 1 {
			if hasSDBlobHash && hasSDBlobSize {
				if len(blobHashValue) != 96 || blobSizeValue <= 0 || blobSizeValue > 2097152 {
					conn.Close()
					return
				}

				sdBlobHash = sdBlobHashValue
				sdBlobSize = sdBlobSizeValue

				jsonEncoder.Encode(map[string]any{
					"send_sd_blob": false, // TODO: Improve response
				})
				continue
			}
		}

		if blobHash != "" {
			blobData := make([]byte, blobSize)
			_, err := io.ReadFull(conn, blobData)

			//TODO Process blob data
			fmt.Printf("%+v\n", blobData)

			jsonEncoder.Encode(map[string]any{
				"received_blob": err == nil,
			})
			if err != nil {
				conn.Close()
				return
			}

			blobHash = ""
			blobSize = -1
			continue
		}
		if sdBlobHash != "" {
			sdBlobData := make([]byte, sdBlobSize)
			_, err := io.ReadFull(conn, sdBlobData)

			//TODO Process SD blob data
			fmt.Printf("%+v\n", sdBlobData)

			jsonEncoder.Encode(map[string]any{
				"received_sd_blob": err == nil,
			})
			if err != nil {
				conn.Close()
				return
			}

			sdBlobHash = ""
			sdBlobSize = -1
			continue
		}

		conn.Close()
		return
	}
}
