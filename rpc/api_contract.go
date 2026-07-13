package rpc

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
)

// methodSpecs mirrors Daemon's active jsonrpc_* signatures at Python SDK
// commit e7666f489418e96b6d2104974e93915b539235c5.
type methodSpec struct {
	required             []string
	optional             []string
	allowExtra           bool
	allowPositionalExtra bool
	deferMissing         bool
}

type normalizedRPCParams struct {
	ctx             context.Context
	args            []any
	kwargs          map[string]any
	named           map[string]any
	orderedKwargs   []string
	includeProtobuf any
}

type rpcParamsError struct {
	code    int
	message string
	data    map[string]any
}

const paramsOrderKey = "__lbry_params_order"

func newMethodSpec(required, optional string, allowExtra, allowPositionalExtra bool) methodSpec {
	return methodSpec{
		required:             strings.Fields(required),
		optional:             strings.Fields(optional),
		allowExtra:           allowExtra,
		allowPositionalExtra: allowPositionalExtra,
	}
}

func newDeferredMissingMethodSpec(
	required, optional string, allowExtra, allowPositionalExtra bool,
) methodSpec {
	spec := newMethodSpec(required, optional, allowExtra, allowPositionalExtra)
	spec.deferMissing = true
	return spec
}

var deprecatedMethods = map[string]string{
	"channel_new": "channel_create",
}

var methodSpecs = map[string]methodSpec{
	"stop":                    newMethodSpec("", "", false, false),
	"ffmpeg_find":             newMethodSpec("", "", false, false),
	"status":                  newMethodSpec("", "", false, false),
	"version":                 newMethodSpec("", "", false, false),
	"resolve":                 newMethodSpec("urls", "wallet_id", true, true),
	"get":                     newMethodSpec("uri", "file_name download_directory timeout save_file wallet_id", false, false),
	"settings_get":            newMethodSpec("", "", false, false),
	"settings_set":            newMethodSpec("key value", "", false, false),
	"settings_clear":          newMethodSpec("key", "", false, false),
	"preference_get":          newMethodSpec("", "key wallet_id", false, false),
	"preference_set":          newMethodSpec("key value", "wallet_id", false, false),
	"wallet_list":             newMethodSpec("", "wallet_id page page_size", false, false),
	"wallet_reconnect":        newMethodSpec("", "", false, false),
	"wallet_create":           newMethodSpec("wallet_id", "skip_on_startup create_account single_key", false, false),
	"wallet_export":           newMethodSpec("", "password wallet_id", false, false),
	"wallet_import":           newMethodSpec("data", "password wallet_id blocking", false, false),
	"wallet_add":              newMethodSpec("wallet_id", "", false, false),
	"wallet_remove":           newMethodSpec("wallet_id", "", false, false),
	"wallet_balance":          newMethodSpec("", "wallet_id confirmations", false, false),
	"wallet_status":           newMethodSpec("", "wallet_id", false, false),
	"wallet_unlock":           newMethodSpec("password", "wallet_id", false, false),
	"wallet_lock":             newMethodSpec("", "wallet_id", false, false),
	"wallet_decrypt":          newMethodSpec("", "wallet_id", false, false),
	"wallet_encrypt":          newMethodSpec("new_password", "wallet_id", false, false),
	"wallet_send":             newMethodSpec("amount addresses", "wallet_id change_account_id funding_account_ids preview blocking", false, false),
	"account_list":            newMethodSpec("", "account_id wallet_id confirmations include_claims show_seed page page_size", false, false),
	"account_balance":         newMethodSpec("", "account_id wallet_id confirmations", false, false),
	"account_add":             newMethodSpec("account_name", "wallet_id single_key seed private_key public_key", false, false),
	"account_create":          newMethodSpec("account_name", "single_key wallet_id", false, false),
	"account_remove":          newMethodSpec("account_id", "wallet_id", false, false),
	"account_set":             newMethodSpec("account_id", "wallet_id default new_name change_gap change_max_uses receiving_gap receiving_max_uses", false, false),
	"account_max_address_gap": newMethodSpec("account_id", "wallet_id", false, false),
	"account_fund":            newMethodSpec("", "to_account from_account amount everything outputs broadcast wallet_id", false, false),
	"account_deposit":         newMethodSpec("txid nout redeem_script private_key", "to_account wallet_id preview blocking", false, false),
	"account_send":            newMethodSpec("amount addresses", "account_id wallet_id preview blocking", false, false),
	"sync_hash":               newMethodSpec("", "wallet_id", false, false),
	"sync_apply":              newMethodSpec("password", "data wallet_id blocking", false, false),
	"address_is_mine":         newMethodSpec("address", "account_id wallet_id", false, false),
	"address_list":            newMethodSpec("", "address account_id wallet_id page page_size", false, false),
	"address_unused":          newMethodSpec("", "account_id wallet_id", false, false),
	"file_list":               newMethodSpec("", "sort reverse comparison wallet_id page page_size", true, false),
	"file_set_status":         newMethodSpec("status", "", true, false),
	"file_delete":             newMethodSpec("", "delete_from_download_dir delete_all", true, false),
	"file_save":               newMethodSpec("", "file_name download_directory", true, false),
	"purchase_list":           newMethodSpec("", "claim_id resolve account_id wallet_id page page_size", false, false),
	"purchase_create":         newMethodSpec("", "claim_id url wallet_id funding_account_ids allow_duplicate_purchase override_max_key_fee preview blocking", false, false),
	"claim_list":              newMethodSpec("", "claim_type", true, false),
	"support_sum":             newMethodSpec("claim_id new_sdk_server", "include_channel_content", true, false),
	"claim_search":            newMethodSpec("", "", true, true),
	"channel_create":          newMethodSpec("name bid", "allow_duplicate_name account_id wallet_id claim_address funding_account_ids preview blocking", true, false),
	"channel_update":          newMethodSpec("claim_id", "bid account_id wallet_id claim_address funding_account_ids new_signing_key preview blocking replace", true, false),
	"channel_sign":            newMethodSpec("", "channel_name channel_id hexdata salt channel_account_id wallet_id", false, false),
	"channel_abandon":         newMethodSpec("", "claim_id txid nout account_id wallet_id preview blocking", false, false),
	"channel_list":            newMethodSpec("", "", true, true),
	"channel_export":          newMethodSpec("", "channel_id channel_name account_id wallet_id", false, false),
	"channel_import":          newMethodSpec("channel_data", "wallet_id", false, false),
	"publish":                 newMethodSpec("name", "", true, false),
	"stream_repost":           newMethodSpec("name bid claim_id", "allow_duplicate_name channel_id channel_name channel_account_id account_id wallet_id claim_address funding_account_ids preview blocking", true, false),
	"stream_create":           newMethodSpec("name bid", "file_path allow_duplicate_name channel_id channel_name channel_account_id account_id wallet_id claim_address funding_account_ids preview blocking validate_file optimize_file", true, false),
	"stream_update":           newMethodSpec("claim_id", "bid file_path channel_id channel_name channel_account_id clear_channel account_id wallet_id claim_address funding_account_ids preview blocking replace validate_file optimize_file", true, false),
	"stream_abandon":          newMethodSpec("", "claim_id txid nout account_id wallet_id preview blocking", false, false),
	"stream_list":             newMethodSpec("", "", true, true),
	"stream_cost_estimate":    newMethodSpec("uri", "", false, false),
	"collection_create":       newMethodSpec("name bid claims", "allow_duplicate_name channel_id channel_name channel_account_id account_id wallet_id claim_address funding_account_ids preview blocking", true, false),
	"collection_update":       newMethodSpec("claim_id", "bid channel_id channel_name channel_account_id clear_channel account_id wallet_id claim_address funding_account_ids preview blocking replace", true, false),
	"collection_abandon":      newMethodSpec("", "", true, true),
	"collection_list":         newMethodSpec("", "resolve_claims resolve account_id wallet_id page page_size", false, false),
	"collection_resolve":      newMethodSpec("", "claim_id url wallet_id page page_size", false, false),
	"support_create":          newMethodSpec("claim_id amount", "tip channel_id channel_name channel_account_id account_id wallet_id funding_account_ids comment preview blocking", false, false),
	"support_list":            newMethodSpec("", "", true, true),
	"support_abandon":         newMethodSpec("", "claim_id txid nout keep account_id wallet_id preview blocking", false, false),
	"transaction_list":        newMethodSpec("", "account_id wallet_id page page_size", false, false),
	"transaction_show":        newDeferredMissingMethodSpec("txid", "", false, false),
	"txo_list":                newMethodSpec("", "account_id wallet_id page page_size resolve order_by no_totals include_received_tips", true, false),
	"txo_spend":               newMethodSpec("", "account_id wallet_id batch_size include_full_tx preview blocking", true, false),
	"txo_sum":                 newMethodSpec("", "account_id wallet_id", true, false),
	"txo_plot":                newMethodSpec("", "account_id wallet_id days_back start_day days_after end_day", true, false),
	"utxo_list":               newMethodSpec("", "", true, true),
	"utxo_release":            newMethodSpec("", "account_id wallet_id", false, false),
	"blob_get":                newMethodSpec("blob_hash", "timeout read", false, false),
	"blob_delete":             newMethodSpec("blob_hash", "", false, false),
	"peer_list":               newMethodSpec("blob_hash", "page page_size", false, false),
	"blob_announce":           newMethodSpec("", "blob_hash stream_hash sd_hash", false, false),
	"blob_list":               newMethodSpec("", "uri stream_hash sd_hash needed finished page page_size", false, false),
	"blob_reflect":            newMethodSpec("blob_hashes", "reflector_server", false, false),
	"blob_reflect_all":        newMethodSpec("", "", false, false),
	"blob_clean":              newMethodSpec("", "", false, false),
	"file_reflect":            newMethodSpec("", "", true, false),
	"peer_ping":               newMethodSpec("node_id address port", "", false, false),
	"routing_table_get":       newMethodSpec("", "", false, false),
	"tracemalloc_enable":      newMethodSpec("", "", false, false),
	"tracemalloc_disable":     newMethodSpec("", "", false, false),
	"tracemalloc_top":         newMethodSpec("", "items", false, false),
}

func normalizeRPCParams(
	message map[string]any, spec methodSpec, requestedMethod, callableMethod string,
) (normalizedRPCParams, *rpcParamsError) {
	rawParams, hasParams := message["params"]
	if !hasParams {
		rawParams = map[string]any{}
	}

	params := normalizedRPCParams{
		ctx: context.Background(), args: []any{}, kwargs: map[string]any{}, named: map[string]any{},
	}
	if orderedNames, ok := message[paramsOrderKey].([]string); ok {
		params.orderedKwargs = append([]string(nil), orderedNames...)
	}
	switch value := rawParams.(type) {
	case map[string]any:
		params.kwargs = make(map[string]any, len(value))
		for name, item := range value {
			if name == "include_protobuf" {
				params.includeProtobuf = item
			} else {
				params.kwargs[name] = item
			}
		}
	case []any:
		switch {
		case len(value) == 0:
		case len(value) == 1:
			kwargs, ok := value[0].(map[string]any)
			if !ok {
				return params, invalidParamsFormat(rawParams)
			}
			params.kwargs = kwargs
		case len(value) == 2:
			args, argsOK := value[0].([]any)
			kwargs, kwargsOK := value[1].(map[string]any)
			if !argsOK || !kwargsOK {
				return params, invalidParamsFormat(rawParams)
			}
			params.args = args
			params.kwargs = kwargs
		default:
			return params, invalidParamsFormat(rawParams)
		}
	default:
		return params, invalidParamsFormat(rawParams)
	}

	allNames := append(append([]string{}, spec.required...), spec.optional...)
	duplicate := make([]string, 0)
	for index := 0; index < len(params.args) && index < len(allNames); index++ {
		if _, exists := params.kwargs[allNames[index]]; exists {
			duplicate = append(duplicate, allNames[index])
		}
	}
	if len(duplicate) > 0 {
		return params, parameterError("Duplicate parameters", requestedMethod, duplicate)
	}

	// Preserve the Python SDK's -0 slice quirk: signatures with no optional
	// defaults reach the method call and fail there instead of returning -32602.
	missing := make([]string, 0)
	if len(spec.optional) > 0 {
		for index, name := range spec.required {
			if index < len(params.args) {
				continue
			}
			if _, exists := params.kwargs[name]; !exists {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			return params, parameterError("Missing required parameters", requestedMethod, missing)
		}
	} else {
		missing = missingRequiredParameters(params, spec)
	}

	if !spec.allowExtra {
		allowed := make(map[string]struct{}, len(allNames))
		for _, name := range allNames {
			allowed[name] = struct{}{}
		}
		extra := make([]string, 0)
		seen := make(map[string]struct{}, len(params.kwargs))
		orderedNames, _ := message[paramsOrderKey].([]string)
		for _, name := range orderedNames {
			if _, exists := allowed[name]; !exists {
				if _, present := params.kwargs[name]; present {
					extra = append(extra, name)
					seen[name] = struct{}{}
				}
			}
		}
		unorderedExtra := make([]string, 0)
		for name := range params.kwargs {
			if _, alreadySeen := seen[name]; alreadySeen {
				continue
			}
			if _, exists := allowed[name]; !exists {
				unorderedExtra = append(unorderedExtra, name)
			}
		}
		sort.Strings(unorderedExtra)
		extra = append(extra, unorderedExtra...)
		if len(extra) > 0 {
			return params, parameterError("Extraneous parameters", requestedMethod, extra)
		}
	}
	if len(missing) > 0 && !spec.deferMissing {
		return params, applicationTypeError(
			requestedMethod,
			params,
			formatMissingArguments(callableMethod, missing),
		)
	}

	if len(params.args) > len(allNames) && !spec.allowPositionalExtra {
		return params, applicationTypeError(
			requestedMethod,
			params,
			formatTooManyArguments(callableMethod, spec, len(params.args)),
		)
	}

	for index, value := range params.args {
		if index < len(allNames) {
			params.named[allNames[index]] = value
		}
	}
	for name, value := range params.kwargs {
		params.named[name] = value
	}
	return params, nil
}

func missingRequiredParameters(params normalizedRPCParams, spec methodSpec) []string {
	missing := make([]string, 0)
	for index, name := range spec.required {
		if index < len(params.args) {
			continue
		}
		if _, exists := params.kwargs[name]; !exists {
			missing = append(missing, name)
		}
	}
	return missing
}

func applicationTypeError(method string, params normalizedRPCParams, message string) *rpcParamsError {
	return &rpcParamsError{
		code:    -32500,
		message: message,
		data: map[string]any{
			"args":      params.args,
			"command":   method,
			"kwargs":    redactPassword(params.kwargs),
			"name":      "TypeError",
			"traceback": currentTraceback(),
		},
	}
}

func formatMissingArguments(method string, missing []string) string {
	quoted := make([]string, len(missing))
	for index, name := range missing {
		quoted[index] = "'" + name + "'"
	}
	var names string
	switch len(quoted) {
	case 1:
		names = quoted[0]
	case 2:
		names = quoted[0] + " and " + quoted[1]
	default:
		names = strings.Join(quoted[:len(quoted)-1], ", ") + ", and " + quoted[len(quoted)-1]
	}
	noun := "arguments"
	if len(missing) == 1 {
		noun = "argument"
	}
	return fmt.Sprintf(
		"Daemon.jsonrpc_%s() missing %d required positional %s: %s",
		method, len(missing), noun, names,
	)
}

func formatTooManyArguments(method string, spec methodSpec, provided int) string {
	minimum := len(spec.required) + 1
	maximum := len(spec.required) + len(spec.optional) + 1
	given := provided + 1
	if minimum == maximum {
		noun := "arguments"
		if maximum == 1 {
			noun = "argument"
		}
		return fmt.Sprintf(
			"Daemon.jsonrpc_%s() takes %d positional %s but %d were given",
			method, maximum, noun, given,
		)
	}
	return fmt.Sprintf(
		"Daemon.jsonrpc_%s() takes from %d to %d positional arguments but %d were given",
		method, minimum, maximum, given,
	)
}

func parameterError(kind, method string, names []string) *rpcParamsError {
	return &rpcParamsError{
		code:    -32602,
		message: fmt.Sprintf("%s for %s command: %s", kind, method, strings.Join(names, ", ")),
	}
}

func invalidParamsFormat(value any) *rpcParamsError {
	return &rpcParamsError{
		code:    -32602,
		message: "Invalid parameters format: " + pythonStr(value),
	}
}

func pythonStr(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return pythonRepr(value)
}

func pythonRepr(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case float64:
		switch {
		case math.IsNaN(typed):
			return "nan"
		case math.IsInf(typed, 1):
			return "inf"
		case math.IsInf(typed, -1):
			return "-inf"
		default:
			return fmt.Sprint(typed)
		}
	case string:
		return pythonStringRepr(typed)
	case []any:
		parts := make([]string, len(typed))
		for index, item := range typed {
			parts[index] = pythonRepr(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for index, key := range keys {
			parts[index] = pythonRepr(key) + ": " + pythonRepr(typed[key])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return fmt.Sprint(value)
	}
}

func pythonStringRepr(value string) string {
	quote := '\''
	if strings.ContainsRune(value, '\'') && !strings.ContainsRune(value, '"') {
		quote = '"'
	}
	var represented strings.Builder
	represented.WriteRune(quote)
	for _, character := range value {
		switch character {
		case '\\':
			represented.WriteString("\\\\")
		case '\t':
			represented.WriteString("\\t")
		case '\n':
			represented.WriteString("\\n")
		case '\r':
			represented.WriteString("\\r")
		default:
			switch {
			case character == quote:
				represented.WriteRune('\\')
				represented.WriteRune(character)
			case unicode.IsPrint(character):
				represented.WriteRune(character)
			case character <= 0xff:
				fmt.Fprintf(&represented, "\\x%02x", character)
			case character <= 0xffff:
				fmt.Fprintf(&represented, "\\u%04x", character)
			default:
				fmt.Fprintf(&represented, "\\U%08x", character)
			}
		}
	}
	represented.WriteRune(quote)
	return represented.String()
}
