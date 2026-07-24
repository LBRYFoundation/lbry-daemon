package rpc

import (
	"reflect"
	"sort"
	"testing"

	"lbry/daemon/legacycli"
)

func TestLegacyCLIAndRPCMethodContractsStayInSync(t *testing.T) {
	rpcMethods := make([]string, 0, len(methodSpecs))
	for method := range methodSpecs {
		rpcMethods = append(rpcMethods, method)
	}
	sort.Strings(rpcMethods)
	if cliMethods := legacycli.ActiveMethods(); !reflect.DeepEqual(cliMethods, rpcMethods) {
		t.Fatalf("legacy CLI methods differ from RPC contract\nCLI: %v\nRPC: %v", cliMethods, rpcMethods)
	}
	if cliDeprecated := legacycli.DeprecatedMethods(); !reflect.DeepEqual(cliDeprecated, deprecatedMethods) {
		t.Fatalf("legacy CLI deprecations = %#v, RPC deprecations = %#v", cliDeprecated, deprecatedMethods)
	}
}
