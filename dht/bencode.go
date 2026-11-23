package dht

import (
	"bytes"
	"strconv"

	"github.com/jackpal/bencode-go"
)

func BencodeDecode(value []byte) (any, error) {
	// Fix invalid B-encoding
	if len(value) > 0 && value[0] == 'd' {
		value[0] = 'l'

		list, err := bencode.NewDecoder(bytes.NewReader(value)).SetStrict(true).Decode()
		if err != nil {
			return nil, err
		}

		listArray, listIsArray := list.([]any)
		if listIsArray {
			for i, item := range listArray {
				if i%2 == 0 {
					key := item
					keyInt, keyIsInt := key.(int64)
					if keyIsInt {
						listArray[i] = strconv.FormatInt(keyInt, 10)
					}
				}
			}
		}

		value, err = BencodeEncode(list)
		if err != nil {
			return nil, err
		}

		value[0] = 'd'
	}

	// Normal B-decoding
	return bencode.NewDecoder(bytes.NewReader(value)).SetStrict(true).Decode()
}

func BencodeEncode(value any) ([]byte, error) {
	var buf bytes.Buffer

	err := bencode.Marshal(&buf, value)

	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
