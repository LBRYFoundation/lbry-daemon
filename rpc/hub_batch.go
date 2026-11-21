package rpc

import "bufio"
import "context"
import "encoding/json"
import "errors"
import "fmt"
import "net"
import "strconv"
import "time"

import "lbry/daemon/wallet"

// hubBatchPerRequest is added to the reply deadline for every request in a batch, since the hub answers
// them one after the other.
const hubBatchPerRequest = 50 * time.Millisecond

// SendJSONBatch sends several JSON-RPC requests to a hub on one connection and returns the replies in the
// order of the requests. Like SendJSON, every address of the host is tried, the ones that failed recently last.
func SendJSONBatch(host string, port int, requests []map[string]any) ([]map[string]any, error) {
	addresses, err := net.DefaultResolver.LookupHost(context.Background(), host)
	if err != nil {
		return nil, err
	}
	for i, address := range addresses {
		addresses[i] = net.JoinHostPort(address, strconv.Itoa(port))
	}
	return SendJSONBatchToAddresses(addresses, requests)
}

// SendJSONBatchToAddresses is SendJSONBatch for known addresses ("host:port"). The requests get the ids 1 to n
// and every one must be answered, or the address counts as failed.
func SendJSONBatchToAddresses(addresses []string, requests []map[string]any) ([]map[string]any, error) {
	if len(requests) == 0 {
		return nil, nil
	}
	lines := make([][]byte, len(requests))
	for i, request := range requests {
		numbered := map[string]any{}
		for key, value := range request {
			numbered[key] = value
		}
		numbered["id"] = i + 1
		line, err := json.Marshal(numbered)
		if err != nil {
			return nil, err
		}
		lines[i] = append(line, '\n')
	}
	errs := []error{}
	for _, address := range hubAddressHealth.order(addresses) {
		replies, err := sendLinesToAddress(address, lines)
		if err == nil {
			hubAddressHealth.succeeded(address)
			return replies, nil
		}
		hubAddressHealth.fail(address)
		errs = append(errs, fmt.Errorf("%s: %w", address, err))
	}
	if len(errs) == 0 {
		return nil, errors.New("no hub address to try")
	}
	return nil, fmt.Errorf("no hub address answered: %w", errors.Join(errs...))
}

func sendLinesToAddress(address string, lines [][]byte) ([]map[string]any, error) {
	conn, err := net.DialTimeout("tcp", address, HubDialTimeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(HubReplyTimeout + time.Duration(len(lines))*hubBatchPerRequest)); err != nil {
		return nil, err
	}
	writer := bufio.NewWriter(conn)
	for _, line := range lines {
		if _, err := writer.Write(line); err != nil {
			return nil, err
		}
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}

	replies := make([]map[string]any, len(lines))
	reader := bufio.NewReader(conn)
	for answered := 0; answered < len(lines); {
		line, err := reader.ReadBytes('\n')
		if err != nil && len(line) == 0 {
			return nil, fmt.Errorf("the hub answered %d of %d requests: %w", answered, len(lines), err)
		}
		var reply map[string]any
		if err := json.Unmarshal(line, &reply); err != nil {
			return nil, fmt.Errorf("unexpected reply: %w", err)
		}
		id, isNumber := reply["id"].(float64)
		index := int(id) - 1
		if !isNumber || index < 0 || index >= len(lines) || replies[index] != nil {
			return nil, errors.New("the hub sent a reply to a request that was not made")
		}
		replies[index] = reply
		answered++
	}
	return replies, nil
}

// CallMany makes the requests on one connection, like Call does for one.
func (hubClient) CallMany(requests []wallet.HubRequest) ([]wallet.HubResponse, error) {
	messages := make([]map[string]any, len(requests))
	for i, request := range requests {
		messages[i] = map[string]any{"jsonrpc": "2.0", "method": request.Method, "params": request.Params}
	}
	replies, err := SendJSONBatch(TMP_HUB_HOSTNAME, 50001, messages)
	if err != nil {
		return nil, err
	}
	responses := make([]wallet.HubResponse, len(replies))
	for i, reply := range replies {
		responses[i].Result, responses[i].Err = hubResult(requests[i].Method, reply, nil)
	}
	return responses, nil
}
