package rpc

import "sync"
import "time"

// HubFailureMemory is how long an address that failed is tried last. A variable so that tests can shorten it.
var HubFailureMemory = 5 * time.Minute

// hubHealth remembers which hub addresses failed, so a dead address (the hub's DNS name has one that accepts
// no connection) is not waited on for every call: it is tried after the others, and again once the memory
// has run out.
type hubHealth struct {
	mutex  sync.Mutex
	failed map[string]time.Time // address -> when it last failed
}

var hubAddressHealth = &hubHealth{failed: map[string]time.Time{}}

// order returns the addresses with the recently failed ones last, each group in its original order.
func (health *hubHealth) order(addresses []string) []string {
	health.mutex.Lock()
	defer health.mutex.Unlock()
	healthy := make([]string, 0, len(addresses))
	failing := []string{}
	for _, address := range addresses {
		if failedAt, has := health.failed[address]; has && time.Since(failedAt) < HubFailureMemory {
			failing = append(failing, address)
		} else {
			healthy = append(healthy, address)
		}
	}
	return append(healthy, failing...)
}

func (health *hubHealth) fail(address string) {
	health.mutex.Lock()
	defer health.mutex.Unlock()
	health.failed[address] = time.Now()
}

func (health *hubHealth) succeeded(address string) {
	health.mutex.Lock()
	defer health.mutex.Unlock()
	delete(health.failed, address)
}
