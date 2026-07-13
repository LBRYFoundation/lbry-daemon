package keys

import (
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

func decodeSEC1PrivateScalar(encoded []byte) ([]byte, error) {
	fields, err := parseLegacyASN1Sequence(encoded)
	if err != nil {
		return nil, err
	}
	if len(fields) < 2 {
		return nil, fmt.Errorf("SEC1 field count is %d, want at least 2", len(fields))
	}
	if err := expectASN1(fields[0], asn1.ClassUniversal, asn1.TagInteger, false, "SEC1 version"); err != nil {
		return nil, err
	}
	privateKey, err := legacyOctetStringBytes(fields[1], "SEC1 private key")
	if err != nil {
		return nil, err
	}

	index := 2
	if index < len(fields) && fields[index].Class == asn1.ClassContextSpecific && fields[index].Tag == 0 {
		if err := validateExplicitECDomainParameters(fields[index]); err != nil {
			return nil, fmt.Errorf("SEC1 parameters: %w", err)
		}
		index++
	}
	if index < len(fields) && fields[index].Class == asn1.ClassContextSpecific && fields[index].Tag == 1 {
		if err := validateExplicitBitString(fields[index]); err != nil {
			return nil, fmt.Errorf("SEC1 public key: %w", err)
		}
		index++
	}
	// asn1crypto retains unknown trailing fields. A later context-zero field
	// collides with the declared parameters slot; other well-formed fields do
	// not affect PrivateKey.from_pem's scalar lookup.
	for ; index < len(fields); index++ {
		if fields[index].Class == asn1.ClassContextSpecific && fields[index].Tag == 0 {
			return nil, errors.New("unexpected duplicate SEC1 parameters field")
		}
	}
	return privateKey, nil
}

func decodePKCS8PrivateScalar(encoded []byte) ([]byte, error) {
	fields, err := parseLegacyASN1Sequence(encoded)
	if err != nil {
		return nil, err
	}
	if len(fields) < 3 {
		return nil, fmt.Errorf("PKCS#8 field count is %d, want at least 3", len(fields))
	}
	if err := expectASN1(fields[0], asn1.ClassUniversal, asn1.TagInteger, false, "PKCS#8 version"); err != nil {
		return nil, err
	}
	algorithm, err := parseASN1SequenceValue(fields[1], "PKCS#8 algorithm")
	if err != nil {
		return nil, err
	}
	if len(algorithm) < 1 {
		return nil, errors.New("PKCS#8 algorithm has no fields")
	}
	algorithmOID, err := decodeASN1OID(algorithm[0], "PKCS#8 algorithm OID")
	if err != nil {
		return nil, err
	}
	if algorithmOID != oidECPrivateKey.String() {
		return nil, fmt.Errorf("PKCS#8 algorithm is %v, want ecPublicKey", algorithmOID)
	}
	if len(algorithm) >= 2 {
		if err := validateECDomainParameters(algorithm[1]); err != nil {
			return nil, fmt.Errorf("PKCS#8 EC parameters: %w", err)
		}
	}
	privateKeyDER, err := legacyOctetStringBytes(fields[2], "PKCS#8 private key")
	if err != nil {
		return nil, err
	}
	index := 3
	if index < len(fields) && fields[index].Class == asn1.ClassContextSpecific && fields[index].Tag == 0 {
		if err := validatePKCS8Attributes(fields[index]); err != nil {
			return nil, err
		}
		index++
	}
	for ; index < len(fields); index++ {
		if fields[index].Class == asn1.ClassContextSpecific && fields[index].Tag == 0 {
			return nil, errors.New("unexpected duplicate PKCS#8 attributes field")
		}
	}
	return decodeSEC1PrivateScalar(privateKeyDER)
}

func parseLegacyASN1Sequence(encoded []byte) ([]asn1.RawValue, error) {
	normalized, _, err := normalizeBERElement(encoded, 0)
	if err != nil {
		return nil, err
	}
	var sequence asn1.RawValue
	if _, err := asn1.Unmarshal(normalized, &sequence); err != nil {
		return nil, err
	}
	return parseASN1SequenceValue(sequence, "top-level value")
}

func parseASN1SequenceValue(value asn1.RawValue, name string) ([]asn1.RawValue, error) {
	if err := expectASN1(value, asn1.ClassUniversal, asn1.TagSequence, true, name); err != nil {
		return nil, err
	}
	return parseASN1Children(value.Bytes)
}

func parseASN1Children(contents []byte) ([]asn1.RawValue, error) {
	var result []asn1.RawValue
	for len(contents) > 0 {
		normalized, rest, err := normalizeBERElement(contents, 0)
		if err != nil {
			return nil, err
		}
		var value asn1.RawValue
		if _, err := asn1.Unmarshal(normalized, &value); err != nil {
			return nil, err
		}
		if len(rest) >= len(contents) {
			return nil, errors.New("ASN.1 parser made no progress")
		}
		result = append(result, value)
		contents = rest
	}
	return result, nil
}

func expectASN1(value asn1.RawValue, class, tag int, compound bool, name string) error {
	if value.Class != class || value.Tag != tag || value.IsCompound != compound {
		return fmt.Errorf("%s has class %d tag %d compound %t", name, value.Class, value.Tag, value.IsCompound)
	}
	return nil
}

func decodeASN1OID(value asn1.RawValue, name string) (string, error) {
	if err := expectASN1(value, asn1.ClassUniversal, asn1.TagOID, false, name); err != nil {
		return "", err
	}
	// asn1crypto accepts non-minimal base-128 arcs and drops an unfinished
	// final arc. Preserve that behavior before comparing mapped OIDs.
	components := make([]big.Int, 0, len(value.Bytes))
	var component big.Int
	for _, encoded := range value.Bytes {
		component.Lsh(&component, 7)
		component.Or(&component, big.NewInt(int64(encoded&0x7f)))
		if encoded&0x80 == 0 {
			components = append(components, *new(big.Int).Set(&component))
			component.SetInt64(0)
		}
	}
	if len(components) == 0 {
		return "", nil
	}
	first := new(big.Int).Set(&components[0])
	forty := big.NewInt(40)
	eighty := big.NewInt(80)
	arcs := make([]string, 0, len(components)+1)
	switch {
	case first.Cmp(forty) < 0:
		arcs = append(arcs, "0", first.String())
	case first.Cmp(eighty) < 0:
		arcs = append(arcs, "1", first.Sub(first, forty).String())
	default:
		arcs = append(arcs, "2", first.Sub(first, eighty).String())
	}
	for index := 1; index < len(components); index++ {
		arcs = append(arcs, components[index].String())
	}
	return strings.Join(arcs, "."), nil
}

func validateExplicitECDomainParameters(value asn1.RawValue) error {
	if err := expectASN1(value, asn1.ClassContextSpecific, 0, true, "explicit EC parameters"); err != nil {
		return err
	}
	child, err := parseFirstASN1Child(value.Bytes)
	if err != nil {
		return err
	}
	return validateECDomainParameters(child)
}

func validateECDomainParameters(value asn1.RawValue) error {
	if value.Class != asn1.ClassUniversal {
		return fmt.Errorf("EC parameters have class %d, want universal", value.Class)
	}
	switch value.Tag {
	case asn1.TagOID:
		_, err := decodeASN1OID(value, "named curve")
		return err
	case asn1.TagNull:
		if value.IsCompound {
			return errors.New("implicit-CA EC parameters are not a primitive NULL")
		}
		return nil
	case asn1.TagSequence:
		return validateSpecifiedECDomain(value)
	default:
		return fmt.Errorf("EC parameters have tag %d", value.Tag)
	}
}

func validateSpecifiedECDomain(value asn1.RawValue) error {
	fields, err := parseASN1SequenceValue(value, "specified EC domain")
	if err != nil {
		return err
	}
	if len(fields) < 5 || len(fields) > 7 {
		return fmt.Errorf("specified EC domain has %d fields, want 5 to 7", len(fields))
	}
	if err := expectASN1(fields[0], asn1.ClassUniversal, asn1.TagInteger, false, "EC domain version"); err != nil {
		return err
	}
	if err := validateECFieldID(fields[1]); err != nil {
		return err
	}
	if err := validateECCurve(fields[2]); err != nil {
		return err
	}
	if _, err := legacyOctetStringBytes(fields[3], "EC base point"); err != nil {
		return err
	}
	if err := expectASN1(fields[4], asn1.ClassUniversal, asn1.TagInteger, false, "EC order"); err != nil {
		return err
	}
	index := 5
	if index < len(fields) && fields[index].Class == asn1.ClassUniversal && fields[index].Tag == asn1.TagInteger && !fields[index].IsCompound {
		index++
	}
	if index < len(fields) {
		if err := validateAlgorithmIdentifier(fields[index], "EC domain hash"); err != nil {
			return err
		}
		index++
	}
	if index != len(fields) {
		return errors.New("unexpected specified EC domain field")
	}
	return nil
}

var (
	oidPrimeField             = "1.2.840.10045.1.1"
	oidCharacteristicTwoField = "1.2.840.10045.1.2"
	oidGaussianNormalBasis    = "1.2.840.10045.1.2.1.1"
	oidTrinomialBasis         = "1.2.840.10045.1.2.1.2"
	oidPentanomialBasis       = "1.2.840.10045.1.2.1.3"
)

func validateECFieldID(value asn1.RawValue) error {
	fields, err := parseASN1SequenceValue(value, "EC field ID")
	if err != nil {
		return err
	}
	if len(fields) != 2 {
		return fmt.Errorf("EC field ID has %d fields, want 2", len(fields))
	}
	fieldType, err := decodeASN1OID(fields[0], "EC field type")
	if err != nil {
		return err
	}
	switch {
	case fieldType == oidPrimeField:
		return expectASN1(fields[1], asn1.ClassUniversal, asn1.TagInteger, false, "prime-field parameters")
	case fieldType == oidCharacteristicTwoField:
		return validateCharacteristicTwoField(fields[1])
	default:
		return fmt.Errorf("unknown EC field type %v", fieldType)
	}
}

func validateCharacteristicTwoField(value asn1.RawValue) error {
	fields, err := parseASN1SequenceValue(value, "characteristic-two field")
	if err != nil {
		return err
	}
	if len(fields) != 3 {
		return fmt.Errorf("characteristic-two field has %d fields, want 3", len(fields))
	}
	if err := expectASN1(fields[0], asn1.ClassUniversal, asn1.TagInteger, false, "characteristic-two m"); err != nil {
		return err
	}
	basis, err := decodeASN1OID(fields[1], "characteristic-two basis")
	if err != nil {
		return err
	}
	switch {
	case basis == oidGaussianNormalBasis:
		return expectASN1(fields[2], asn1.ClassUniversal, asn1.TagNull, false, "Gaussian-normal parameters")
	case basis == oidTrinomialBasis:
		return expectASN1(fields[2], asn1.ClassUniversal, asn1.TagInteger, false, "trinomial parameters")
	case basis == oidPentanomialBasis:
		powers, err := parseASN1SequenceValue(fields[2], "pentanomial parameters")
		if err != nil {
			return err
		}
		if len(powers) != 3 {
			return fmt.Errorf("pentanomial parameters have %d powers, want 3", len(powers))
		}
		for _, power := range powers {
			if err := expectASN1(power, asn1.ClassUniversal, asn1.TagInteger, false, "pentanomial power"); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown characteristic-two basis %v", basis)
	}
}

func validateECCurve(value asn1.RawValue) error {
	fields, err := parseASN1SequenceValue(value, "EC curve")
	if err != nil {
		return err
	}
	if len(fields) < 2 || len(fields) > 3 {
		return fmt.Errorf("EC curve has %d fields, want 2 or 3", len(fields))
	}
	for index := 0; index < 2; index++ {
		if _, err := legacyOctetStringBytes(fields[index], "EC curve coefficient"); err != nil {
			return err
		}
	}
	if len(fields) == 3 {
		return validateBitString(fields[2], "EC curve seed")
	}
	return nil
}

func validateExplicitBitString(value asn1.RawValue) error {
	if err := expectASN1(value, asn1.ClassContextSpecific, 1, true, "explicit public key"); err != nil {
		return err
	}
	child, err := parseFirstASN1Child(value.Bytes)
	if err != nil {
		return err
	}
	return validateBitString(child, "public key bit string")
}

func validateBitString(value asn1.RawValue, name string) error {
	if value.Class != asn1.ClassUniversal || value.Tag != asn1.TagBitString {
		return fmt.Errorf("%s has class %d tag %d", name, value.Class, value.Tag)
	}
	if value.IsCompound {
		return fmt.Errorf("%s uses a definite constructed encoding", name)
	}
	if len(value.Bytes) == 0 {
		return fmt.Errorf("%s has no unused-bits byte", name)
	}
	unused := value.Bytes[0]
	if unused > 7 || (len(value.Bytes) == 1 && unused != 0) {
		return fmt.Errorf("%s has %d unused bits", name, unused)
	}
	return nil
}

func legacyOctetStringBytes(value asn1.RawValue, name string) ([]byte, error) {
	if value.Class != asn1.ClassUniversal || value.Tag != asn1.TagOctetString {
		return nil, fmt.Errorf("%s has class %d tag %d", name, value.Class, value.Tag)
	}
	if !value.IsCompound {
		return append([]byte(nil), value.Bytes...), nil
	}
	return nil, fmt.Errorf("%s uses a definite constructed encoding", name)
}

func parseFirstASN1Child(contents []byte) (asn1.RawValue, error) {
	normalized, _, err := normalizeBERElement(contents, 0)
	if err != nil {
		return asn1.RawValue{}, err
	}
	var child asn1.RawValue
	if _, err := asn1.Unmarshal(normalized, &child); err != nil {
		return asn1.RawValue{}, err
	}
	return child, nil
}

func validateAlgorithmIdentifier(value asn1.RawValue, name string) error {
	fields, err := parseASN1SequenceValue(value, name)
	if err != nil {
		return err
	}
	if len(fields) < 1 {
		return fmt.Errorf("%s has no fields", name)
	}
	_, err = decodeASN1OID(fields[0], name+" OID")
	return err
}

func validatePKCS8Attributes(value asn1.RawValue) error {
	if err := expectASN1(value, asn1.ClassContextSpecific, 0, true, "PKCS#8 attributes"); err != nil {
		return err
	}
	attributes, err := parseASN1Children(value.Bytes)
	if err != nil {
		return err
	}
	for _, attribute := range attributes {
		fields, err := parseASN1SequenceValue(attribute, "PKCS#8 attribute")
		if err != nil {
			return err
		}
		if len(fields) != 2 {
			return fmt.Errorf("PKCS#8 attribute has %d fields, want 2", len(fields))
		}
		if _, err := decodeASN1OID(fields[0], "PKCS#8 attribute OID"); err != nil {
			return err
		}
		if err := expectASN1(fields[1], asn1.ClassUniversal, asn1.TagSet, true, "PKCS#8 attribute values"); err != nil {
			return err
		}
		if _, err := parseASN1Children(fields[1].Bytes); err != nil {
			return err
		}
	}
	return nil
}

func normalizeBERElement(encoded []byte, depth int) ([]byte, []byte, error) {
	if depth > 64 {
		return nil, nil, errors.New("ASN.1 nesting exceeds 64 levels")
	}
	if len(encoded) < 2 {
		return nil, nil, errors.New("truncated ASN.1 element")
	}
	class := int(encoded[0] >> 6)
	compound := encoded[0]&0x20 != 0
	tag := int(encoded[0] & 0x1f)
	offset := 1
	if tag == 0x1f {
		tag = 0
		firstOctet := true
		for {
			if offset >= len(encoded) {
				return nil, nil, errors.New("truncated ASN.1 high tag")
			}
			value := encoded[offset]
			offset++
			if firstOctet && value == 0x80 {
				return nil, nil, errors.New("non-minimal ASN.1 high tag")
			}
			firstOctet = false
			if tag > (int(^uint(0)>>1) >> 7) {
				return nil, nil, errors.New("ASN.1 tag overflows int")
			}
			tag = tag<<7 | int(value&0x7f)
			if value&0x80 == 0 {
				break
			}
		}
		if tag < 31 {
			return nil, nil, errors.New("non-minimal ASN.1 high tag")
		}
	}
	if offset >= len(encoded) {
		return nil, nil, errors.New("truncated ASN.1 length")
	}
	lengthByte := encoded[offset]
	offset++
	indefinite := lengthByte == 0x80
	length := 0
	if lengthByte&0x80 == 0 {
		length = int(lengthByte)
	} else if !indefinite {
		lengthBytes := int(lengthByte & 0x7f)
		if lengthBytes == 0 || lengthBytes > 8 || offset+lengthBytes > len(encoded) {
			return nil, nil, errors.New("invalid ASN.1 long length")
		}
		for _, value := range encoded[offset : offset+lengthBytes] {
			if length > (int(^uint(0)>>1)-int(value))/256 {
				return nil, nil, errors.New("ASN.1 length overflows int")
			}
			length = length*256 + int(value)
		}
		offset += lengthBytes
	}
	if indefinite && !compound {
		return nil, nil, errors.New("primitive ASN.1 element has indefinite length")
	}

	var contents []byte
	var rest []byte
	if indefinite {
		cursor := encoded[offset:]
		for {
			if len(cursor) < 2 {
				return nil, nil, errors.New("unterminated indefinite ASN.1 element")
			}
			if cursor[0] == 0 && cursor[1] == 0 {
				rest = cursor[2:]
				break
			}
			child, childRest, err := normalizeBERElement(cursor, depth+1)
			if err != nil {
				return nil, nil, err
			}
			contents = append(contents, child...)
			cursor = childRest
		}
		if class == asn1.ClassUniversal {
			switch tag {
			case asn1.TagOctetString:
				flattened, flattenErr := flattenIndefiniteOctetString(contents)
				if flattenErr != nil {
					return nil, nil, flattenErr
				}
				contents = flattened
				compound = false
			case asn1.TagBitString:
				flattened, flattenErr := flattenIndefiniteBitString(contents)
				if flattenErr != nil {
					return nil, nil, flattenErr
				}
				contents = flattened
				compound = false
			}
		}
	} else {
		if length > len(encoded)-offset {
			return nil, nil, errors.New("ASN.1 length exceeds input")
		}
		rawContents := encoded[offset : offset+length]
		rest = encoded[offset+length:]
		// Keep definite constructed contents opaque. Schema readers normalize
		// each child when they consume it, while explicit fields intentionally
		// ignore bytes after their first child like asn1crypto's non-strict load.
		contents = append(contents, rawContents...)
	}

	normalized := appendBERIdentifier(nil, class, tag, compound)
	normalized = appendBERLength(normalized, len(contents))
	normalized = append(normalized, contents...)
	return normalized, rest, nil
}

func flattenIndefiniteOctetString(contents []byte) ([]byte, error) {
	var flattened []byte
	for len(contents) > 0 {
		var fragment asn1.RawValue
		rest, err := asn1.Unmarshal(contents, &fragment)
		if err != nil {
			return nil, err
		}
		if fragment.Class != asn1.ClassUniversal || fragment.Tag != asn1.TagOctetString || fragment.IsCompound {
			return nil, errors.New("constructed OCTET STRING contains a non-primitive fragment")
		}
		flattened = append(flattened, fragment.Bytes...)
		contents = rest
	}
	return flattened, nil
}

func flattenIndefiniteBitString(contents []byte) ([]byte, error) {
	var fragments []asn1.RawValue
	for len(contents) > 0 {
		var fragment asn1.RawValue
		rest, err := asn1.Unmarshal(contents, &fragment)
		if err != nil {
			return nil, err
		}
		if fragment.Class != asn1.ClassUniversal || fragment.Tag != asn1.TagBitString ||
			fragment.IsCompound || len(fragment.Bytes) == 0 || fragment.Bytes[0] > 7 {
			return nil, errors.New("constructed BIT STRING contains an invalid fragment")
		}
		fragments = append(fragments, fragment)
		contents = rest
	}
	if len(fragments) == 0 {
		return []byte{0}, nil
	}
	flattened := []byte{fragments[len(fragments)-1].Bytes[0]}
	for index, fragment := range fragments {
		if index < len(fragments)-1 && fragment.Bytes[0] != 0 {
			return nil, errors.New("non-final BIT STRING fragment has unused bits")
		}
		flattened = append(flattened, fragment.Bytes[1:]...)
	}
	return flattened, nil
}

func appendBERIdentifier(destination []byte, class, tag int, compound bool) []byte {
	first := byte(class << 6)
	if compound {
		first |= 0x20
	}
	if tag < 31 {
		return append(destination, first|byte(tag))
	}
	destination = append(destination, first|0x1f)
	var encoded [10]byte
	index := len(encoded)
	for {
		index--
		encoded[index] = byte(tag & 0x7f)
		tag >>= 7
		if tag == 0 {
			break
		}
	}
	start := index
	for index := start; index < len(encoded)-1; index++ {
		encoded[index] |= 0x80
	}
	return append(destination, encoded[start:]...)
}

func appendBERLength(destination []byte, length int) []byte {
	if length < 128 {
		return append(destination, byte(length))
	}
	var encoded [8]byte
	index := len(encoded)
	for length > 0 {
		index--
		encoded[index] = byte(length)
		length >>= 8
	}
	destination = append(destination, 0x80|byte(len(encoded)-index))
	return append(destination, encoded[index:]...)
}
