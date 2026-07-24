package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"
)

type rpcPythonError struct {
	name    string
	message string
}

func (err *rpcPythonError) Error() string {
	return err.message
}

func (err *rpcPythonError) PythonErrorName() string {
	return err.name
}

func (rpcServer *RPCServer) handleSettingsGet(w http.ResponseWriter, _ any) {
	sendResultResponse(w, rpcServer.settings.Snapshot())
}

func (rpcServer *RPCServer) handleSettingsSet(w http.ResponseWriter, rawParams any) {
	params := rawParams.(normalizedRPCParams)
	key := settingKey(params.named["key"])
	value := params.named["value"]
	if text, ok := value.(string); ok && text != "" && (text[0] == '[' || text[0] == '{') {
		value = decodeSettingJSON(text)
	}
	cleaned, err := rpcServer.settings.Set(key, value)
	if err != nil {
		panic(err)
	}
	sendResultResponse(w, map[string]any{key: cleaned})
}

func (rpcServer *RPCServer) handleSettingsClear(w http.ResponseWriter, rawParams any) {
	params := rawParams.(normalizedRPCParams)
	key := settingKey(params.named["key"])
	value, err := rpcServer.settings.Clear(key)
	if err != nil {
		panic(err)
	}
	sendResultResponse(w, map[string]any{key: value})
}

func settingKey(value any) string {
	key, ok := value.(string)
	if ok {
		return key
	}
	panic(&rpcPythonError{
		name:    "TypeError",
		message: fmt.Sprintf("attribute name must be string, not '%s'", pythonTypeName(value)),
	})
}

func pythonTypeName(value any) string {
	switch value.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case json.Number:
		if strings.ContainsAny(value.(json.Number).String(), ".eE") {
			return "float"
		}
		return "int"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "int"
	case float32, float64:
		return "float"
	case string:
		return "str"
	case []any:
		return "list"
	case map[string]any:
		return "dict"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func decodeSettingJSON(text string) any {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		panic(&rpcPythonError{name: "JSONDecodeError", message: jsonDecodeErrorMessage(text, err)})
	}
	offset := int(decoder.InputOffset())
	for offset < len(text) && strings.ContainsRune(" \t\r\n", rune(text[offset])) {
		offset++
	}
	if offset < len(text) {
		panic(&rpcPythonError{name: "JSONDecodeError", message: jsonErrorAt(text, offset, "Extra data")})
	}
	return value
}

func jsonDecodeErrorMessage(text string, err error) string {
	offset := int64(len(text) + 1)
	if syntaxError, ok := err.(*json.SyntaxError); ok {
		offset = syntaxError.Offset
	}
	if offset < 1 {
		offset = 1
	}
	if offset > int64(len(text)+1) {
		offset = int64(len(text) + 1)
	}
	reason, adjustedOffset := jsonDecodeReason(text, int(offset-1), err)
	return jsonErrorAt(text, adjustedOffset, reason)
}

func jsonErrorAt(text string, zeroBasedByteOffset int, reason string) string {
	if zeroBasedByteOffset < 0 {
		zeroBasedByteOffset = 0
	}
	if zeroBasedByteOffset > len(text) {
		zeroBasedByteOffset = len(text)
	}
	oneBasedByteOffset := int64(zeroBasedByteOffset + 1)
	char := byteOffsetToRuneOffset(text, oneBasedByteOffset) - 1
	line, column := jsonLineColumn(text, oneBasedByteOffset)
	return fmt.Sprintf("%s: line %d column %d (char %d)", reason, line, column, char)
}

func jsonDecodeReason(text string, offset int, err error) (string, int) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "{" {
		return "Expecting property name enclosed in double quotes", offset
	}
	if syntaxError, ok := err.(*json.SyntaxError); ok {
		message := syntaxError.Error()
		switch {
		case strings.Contains(message, "in string escape code"):
			return `Invalid \escape`, max(0, offset-1)
		case strings.Contains(message, "after object key:value pair"):
			return "Expecting ',' delimiter", offset
		case strings.Contains(message, "after array element"):
			return "Expecting ',' delimiter", offset
		case strings.Contains(message, "looking for beginning of object key string"):
			if previous := previousNonSpace(text, offset); previous >= 0 && text[previous] == ',' && offset < len(text) && text[offset] == '}' {
				return "Illegal trailing comma before end of object", previous
			}
			return "Expecting property name enclosed in double quotes", offset
		case strings.Contains(message, "after object key"):
			return "Expecting ':' delimiter", offset
		case strings.Contains(message, "looking for beginning of value"):
			if previous := previousNonSpace(text, offset); previous >= 0 && text[previous] == ',' && offset < len(text) && text[offset] == ']' {
				return "Illegal trailing comma before end of array", previous
			}
			return "Expecting value", offset
		case strings.Contains(message, "invalid character"):
			return "Expecting value", offset
		}
	}
	return incompleteJSONReason(trimmed), offset
}

func previousNonSpace(text string, before int) int {
	for index := min(before-1, len(text)-1); index >= 0; index-- {
		if !strings.ContainsRune(" \t\r\n", rune(text[index])) {
			return index
		}
	}
	return -1
}

func incompleteJSONReason(trimmed string) string {
	if trimmed == "{" || strings.HasSuffix(trimmed, ",") && strings.LastIndex(trimmed, "{") > strings.LastIndex(trimmed, "[") {
		return "Expecting property name enclosed in double quotes"
	}
	if strings.HasSuffix(trimmed, ":") || trimmed == "[" || strings.HasSuffix(trimmed, "[") || strings.HasSuffix(trimmed, ",") {
		return "Expecting value"
	}
	lastObject := strings.LastIndexByte(trimmed, '{')
	lastArray := strings.LastIndexByte(trimmed, '[')
	if lastObject > lastArray && strings.LastIndexByte(trimmed[lastObject:], ':') < 0 {
		return "Expecting ':' delimiter"
	}
	return "Expecting ',' delimiter"
}

func byteOffsetToRuneOffset(text string, oneBasedByteOffset int64) int {
	byteIndex := int(oneBasedByteOffset - 1)
	if byteIndex < 0 {
		byteIndex = 0
	}
	if byteIndex > len(text) {
		byteIndex = len(text)
	}
	return utf8.RuneCountInString(text[:byteIndex]) + 1
}

func jsonLineColumn(text string, oneBasedByteOffset int64) (int, int) {
	byteIndex := int(oneBasedByteOffset - 1)
	if byteIndex < 0 {
		byteIndex = 0
	}
	if byteIndex > len(text) {
		byteIndex = len(text)
	}
	prefix := text[:byteIndex]
	line := bytes.Count([]byte(prefix), []byte{'\n'}) + 1
	lastNewline := strings.LastIndexByte(prefix, '\n')
	columnText := prefix
	if lastNewline >= 0 {
		columnText = prefix[lastNewline+1:]
	}
	return line, utf8.RuneCountInString(columnText) + 1
}
