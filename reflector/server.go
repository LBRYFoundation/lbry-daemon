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

	for {
		var data map[string]any

		err := jsonDecoder.Decode(&data)
		if err != nil {
			if err == io.EOF {
				fmt.Println("Client disconnected")
				return
			}
			// TODO Handle error (return OR continue)
		}

		fmt.Printf("%+v\n", data)
		jsonEncoder.Encode(map[string]any{})
	}
}
