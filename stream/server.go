package stream

import "bytes"
import "encoding/json"
import "fmt"
import "lbry/daemon/blob"
import "net"
import "net/http"
import "runtime/debug"
import "strings"

type StreamServer struct {
	blobManager blob.BlobManager
	httpServer  http.Server
}

func CreateServer(blobManager blob.BlobManager) StreamServer {
	contentServeMux := http.NewServeMux()

	server := StreamServer{
		blobManager: blobManager,
		httpServer:  http.Server{Handler: contentServeMux},
	}

	contentServeMux.HandleFunc("/stream/{sd_hash}", server.handleStream)

	return server
}

func (contentServer StreamServer) StartServer(listener net.Listener) {
	err := contentServer.httpServer.Serve(listener)
	if err != nil && err != http.ErrServerClosed {
		fmt.Println("Error when starting Stream server.")
	}
}

func (contentServer StreamServer) handleStream(w http.ResponseWriter, req *http.Request) {
	info, _ := debug.ReadBuildInfo()

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Access-Control-Allow-Headers", "Range")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", "Accept-Ranges, Content-Length, Content-Range")
	//w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Server", "LBRYd/"+info.Main.Version)

	if strings.EqualFold(req.Method, "OPTIONS") {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if strings.EqualFold(req.Method, "GET") {
		sdHash := req.PathValue("sd_hash")

		contentServer.handleSDHash(w, req, sdHash)
		return
	}

	http.Error(w, "HTTP method not allowed.", http.StatusMethodNotAllowed)
}

func (contentServer StreamServer) handleSDHash(w http.ResponseWriter, req *http.Request, sdHash string) {
	blobData, ok := contentServer.blobManager.Get(sdHash)
	if !ok {
		http.Error(w, "Blob not found.", http.StatusNotFound)
		return
	}

	var streamDescriptor map[string]any
	err := json.NewDecoder(bytes.NewReader(blobData)).Decode(&streamDescriptor)
	if err != nil {
		http.Error(w, "Malformed stream descriptor.", http.StatusInternalServerError)
		return
	}

	// TODO: Reimplement "Range" header

	var concat []byte

	key := streamDescriptor["key"].(string)

	blobs := streamDescriptor["blobs"].([]any)
	for _, blobItem := range blobs {
		blobMap := blobItem.(map[string]any)
		length := int(blobMap["length"].(float64))
		if length == 0 {
			break
		}
		blobNum := int(blobMap["blob_num"].(float64))
		blobHash := blobMap["blob_hash"].(string)

		subBlobData, subOk := contentServer.blobManager.Get(blobHash)
		if !subOk {
			http.Error(w, "Cannot retrieve all blobs.", http.StatusInternalServerError)
			return
		}
		iv := blobMap["iv"].(string)
		decrypted, err := blob.DecryptBlob(subBlobData, key, iv)
		if err != nil {
			http.Error(w, "Error during decryption of blob.", http.StatusInternalServerError)
			return
		}
		fmt.Printf("%d/%d\n", blobNum+1, len(blobs)-1)
		concat = append(concat, decrypted...)
	}

	w.Write(concat)
}
