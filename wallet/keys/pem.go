package keys

import (
	"bytes"
	"encoding/asn1"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidPrivateKeyPEM = errors.New("invalid secp256k1 private key PEM")
	oidECPrivateKey         = asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}
	oidSecp256k1            = asn1.ObjectIdentifier{1, 3, 132, 0, 10}
)

type ecPrivateKeyDER struct {
	Version    int
	PrivateKey []byte
}

type privateKeyAlgorithmDER struct {
	Algorithm  asn1.ObjectIdentifier
	Parameters asn1.RawValue `asn1:"optional"`
}

type privateKeyInfoDER struct {
	Version    int
	Algorithm  privateKeyAlgorithmDER
	PrivateKey []byte
}

// ToPEM reproduces coincurve 15 PrivateKey.to_pem: unencrypted PKCS#8 with
// secp256k1 parameters and an inner SEC1 key containing the uncompressed point.
func (key *PrivateKey) ToPEM() (string, error) {
	if key == nil || key.key == nil {
		return "", ErrInvalidPrivateKey
	}
	inner, err := asn1.Marshal(struct {
		Version    int
		PrivateKey []byte
		PublicKey  asn1.BitString `asn1:"optional,explicit,tag:1"`
	}{
		Version:    1,
		PrivateKey: key.PrivateKeyBytes(),
		PublicKey: asn1.BitString{
			Bytes:     key.key.PubKey().SerializeUncompressed(),
			BitLength: 65 * 8,
		},
	})
	if err != nil {
		return "", err
	}
	curve, err := asn1.Marshal(oidSecp256k1)
	if err != nil {
		return "", err
	}
	der, err := asn1.Marshal(privateKeyInfoDER{
		Version: 0,
		Algorithm: privateKeyAlgorithmDER{
			Algorithm:  oidECPrivateKey,
			Parameters: asn1.RawValue{FullBytes: curve},
		},
		PrivateKey: inner,
	})
	if err != nil {
		return "", err
	}
	return encodeCoincurvePEM(der), nil
}

// PrivateKeyFromPEM matches the SDK's SEC1-first, PKCS#8-second loader and
// resets BIP32 metadata to a zero-chain-code root.
func PrivateKeyFromPEM(network Network, value string) (*PrivateKey, error) {
	der, err := decodeCoincurvePEM([]byte(value))
	if err != nil {
		return nil, err
	}
	secret, sec1Err := decodeSEC1PrivateScalar(der)
	if sec1Err != nil {
		secret, err = decodePKCS8PrivateScalar(der)
		if err != nil {
			return nil, fmt.Errorf("%w: SEC1: %v; PKCS#8: %v", ErrInvalidPrivateKeyPEM, sec1Err, err)
		}
	}
	secret, err = padPEMPrivateScalar(secret)
	if err != nil {
		return nil, err
	}
	key, err := NewPrivateKey(network, secret, make([]byte, chainCodeLength), 0, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPrivateKeyPEM, err)
	}
	return key, nil
}

func encodeCoincurvePEM(der []byte) string {
	encoded := base64.StdEncoding.EncodeToString(der)
	var pem strings.Builder
	pem.WriteString("-----BEGIN PRIVATE KEY-----\n")
	for len(encoded) > 64 {
		pem.WriteString(encoded[:64])
		pem.WriteByte('\n')
		encoded = encoded[64:]
	}
	pem.WriteString(encoded)
	pem.WriteString("\n-----END PRIVATE KEY-----\n")
	return pem.String()
}

func decodeCoincurvePEM(value []byte) ([]byte, error) {
	// coincurve's pem_to_der strips outer ASCII whitespace, drops the first
	// and last lines without validating their labels, joins the body, and calls
	// Python's non-strict base64 decoder.
	value = bytes.Trim(value, " \t\n\v\f\r")
	value = bytes.ReplaceAll(value, []byte("\r\n"), []byte("\n"))
	value = bytes.ReplaceAll(value, []byte("\r"), []byte("\n"))
	lines := bytes.Split(value, []byte("\n"))
	var body []byte
	if len(lines) >= 2 {
		body = bytes.Join(lines[1:len(lines)-1], nil)
	}
	decoded, err := decodePEMBase64(body)
	if err != nil {
		return nil, fmt.Errorf("%w: base64: %v", ErrInvalidPrivateKeyPEM, err)
	}
	return decoded, nil
}

func padPEMPrivateScalar(secret []byte) ([]byte, error) {
	for len(secret) > 0 && secret[0] == 0 {
		secret = secret[1:]
	}
	if len(secret) > 32 {
		return nil, fmt.Errorf("%w: private scalar has %d bytes", ErrInvalidPrivateKeyPEM, len(secret))
	}
	padded := make([]byte, 32)
	copy(padded[32-len(secret):], secret)
	return padded, nil
}

func decodePEMBase64(encoded []byte) ([]byte, error) {
	// binascii.a2b_base64(validate=False) discards non-alphabet bytes and
	// misplaced internal padding. Only the run of padding after the final data
	// character participates in completing the last quantum.
	filtered := make([]byte, 0, len(encoded))
	trailingPads := 0
	for _, value := range encoded {
		if value == '=' {
			trailingPads++
			continue
		}
		if _, valid := pemBase64Digit(value); !valid {
			continue
		}
		filtered = append(filtered, value)
		trailingPads = 0
	}

	switch len(filtered) % 4 {
	case 0:
		// Python accepts and ignores any trailing padding after a complete
		// quantum, including input consisting only of padding.
	case 1:
		return nil, errors.New("invalid base64 data: data character count is 1 more than a multiple of 4")
	case 2:
		if trailingPads < 2 {
			return nil, errors.New("invalid base64 data: incorrect padding")
		}
		filtered = append(filtered, '=', '=')
	case 3:
		if trailingPads < 1 {
			return nil, errors.New("invalid base64 data: incorrect padding")
		}
		filtered = append(filtered, '=')
	}

	decoded, err := base64.StdEncoding.DecodeString(string(filtered))
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

func pemBase64Digit(value byte) (byte, bool) {
	switch {
	case value >= 'A' && value <= 'Z':
		return value - 'A', true
	case value >= 'a' && value <= 'z':
		return value - 'a' + 26, true
	case value >= '0' && value <= '9':
		return value - '0' + 52, true
	case value == '+':
		return 62, true
	case value == '/':
		return 63, true
	default:
		return 0, false
	}
}
