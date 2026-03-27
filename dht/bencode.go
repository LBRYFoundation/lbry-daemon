package dht

import "bytes"
import "fmt"
import "sort"
import "strconv"

// Bencode encoder/decoder for LBRY DHT Kademlia protocol.
// Supports: integers, byte strings, lists, dictionaries.

func BencodeEncode(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := bencEncode(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func bencEncode(buf *bytes.Buffer, v any) error {
	switch val := v.(type) {
	case int:
		buf.WriteByte('i')
		buf.WriteString(strconv.Itoa(val))
		buf.WriteByte('e')
	case int64:
		buf.WriteByte('i')
		buf.WriteString(strconv.FormatInt(val, 10))
		buf.WriteByte('e')
	case uint64:
		buf.WriteByte('i')
		buf.WriteString(strconv.FormatUint(val, 10))
		buf.WriteByte('e')
	case []byte:
		buf.WriteString(strconv.Itoa(len(val)))
		buf.WriteByte(':')
		buf.Write(val)
	case string:
		buf.WriteString(strconv.Itoa(len(val)))
		buf.WriteByte(':')
		buf.WriteString(val)
	case []any:
		buf.WriteByte('l')
		for _, item := range val {
			if err := bencEncode(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte('e')
	case map[string]any:
		buf.WriteByte('d')
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			bencEncode(buf, k)
			if err := bencEncode(buf, val[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('e')
	case map[int]any:
		// LBRY DHT uses integer keys in bencode dicts
		buf.WriteByte('d')
		keys := make([]int, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		for _, k := range keys {
			// Encode key as bencode integer (LBRY extension)
			buf.WriteByte('i')
			buf.WriteString(strconv.Itoa(k))
			buf.WriteByte('e')
			if err := bencEncode(buf, val[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('e')
	default:
		return fmt.Errorf("bencode: unsupported type %T", v)
	}
	return nil
}

// BencodeDecode parses bencoded data and returns the value and bytes consumed.
func BencodeDecode(data []byte) (any, int, error) {
	if len(data) == 0 {
		return nil, 0, fmt.Errorf("bencode: empty data")
	}
	switch data[0] {
	case 'i':
		return bencDecodeInt(data)
	case 'l':
		return bencDecodeList(data)
	case 'd':
		return bencDecodeDict(data)
	default:
		if data[0] >= '0' && data[0] <= '9' {
			return bencDecodeBytes(data)
		}
		return nil, 0, fmt.Errorf("bencode: unexpected byte 0x%02x", data[0])
	}
}

func bencDecodeInt(data []byte) (int64, int, error) {
	end := bytes.IndexByte(data, 'e')
	if end < 0 {
		return 0, 0, fmt.Errorf("bencode: unterminated integer")
	}
	n, err := strconv.ParseInt(string(data[1:end]), 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("bencode: invalid integer: %w", err)
	}
	return n, end + 1, nil
}

func bencDecodeBytes(data []byte) ([]byte, int, error) {
	colon := bytes.IndexByte(data, ':')
	if colon < 0 {
		return nil, 0, fmt.Errorf("bencode: missing colon in string")
	}
	length, err := strconv.Atoi(string(data[:colon]))
	if err != nil {
		return nil, 0, fmt.Errorf("bencode: invalid string length: %w", err)
	}
	start := colon + 1
	end := start + length
	if end > len(data) {
		return nil, 0, fmt.Errorf("bencode: string overflow")
	}
	result := make([]byte, length)
	copy(result, data[start:end])
	return result, end, nil
}

func bencDecodeList(data []byte) ([]any, int, error) {
	var list []any
	pos := 1 // skip 'l'
	for pos < len(data) && data[pos] != 'e' {
		val, n, err := BencodeDecode(data[pos:])
		if err != nil {
			return nil, 0, err
		}
		list = append(list, val)
		pos += n
	}
	if pos >= len(data) {
		return nil, 0, fmt.Errorf("bencode: unterminated list")
	}
	return list, pos + 1, nil // +1 for 'e'
}

func bencDecodeDict(data []byte) (map[string]any, int, error) {
	dict := make(map[string]any)
	pos := 1 // skip 'd'
	for pos < len(data) && data[pos] != 'e' {
		// Key can be a byte string OR an integer (LBRY extension)
		var keyStr string
		if data[pos] == 'i' {
			// Integer key (LBRY SDK uses integer dict keys)
			keyInt, n, err := bencDecodeInt(data[pos:])
			if err != nil {
				return nil, 0, fmt.Errorf("bencode: invalid dict int key: %w", err)
			}
			keyStr = strconv.FormatInt(keyInt, 10)
			pos += n
		} else {
			keyBytes, n, err := bencDecodeBytes(data[pos:])
			if err != nil {
				return nil, 0, fmt.Errorf("bencode: invalid dict key: %w", err)
			}
			keyStr = string(keyBytes)
			pos += n
		}

		val, n, err := BencodeDecode(data[pos:])
		if err != nil {
			return nil, 0, err
		}
		dict[keyStr] = val
		pos += n
	}
	if pos >= len(data) {
		return nil, 0, fmt.Errorf("bencode: unterminated dict")
	}
	return dict, pos + 1, nil
}
