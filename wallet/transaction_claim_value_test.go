package wallet

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

const liveClaimValueHex = "000aa7010a8d010a30727c8f4f681de1cee70903ccfbef38dac5d39104e247ec4d7cc597fdafc84fd1d8f89333207d01c16280e1bd4380dd0512175244545f32303236303731315f3130333132352e6d703418a4d18d602209766964656f2f6d7034323036d72ae4e3d594fe090a17e881f53fd2a1acde20dcb64cc495b72c2f1a0f2cd838517b3eb21b54132367e68e4d601a581a044e6f6e65289b90c9d2065a0908800a10ce0518d305421154344e473352314e2041534d522023303352412a3f68747470733a2f2f7468756d62732e6f647963646e2e636f6d2f63633761646235313636383065313436303461313964623061646639626562612e776562705a0461736d725a0b656172206c69636b696e675a0a65617220656174696e675a12633a64697361626c652d636f6d6d656e74735a0f64697361626c652d737570706f727462020801"

func TestDecodeClaimValueLiveStreamFixture(t *testing.T) {
	payload := mustClaimValueHex(t, liveClaimValueHex)
	decoded, err := DecodeClaimValue(payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Type != "stream" || decoded.IsSigned() || decoded.SigningChannelID() != nil {
		t.Fatalf("live claim envelope = type %q signed %v channel %#v", decoded.Type, decoded.IsSigned(), decoded.SigningChannelID())
	}
	assertClaimValueJSON(t, decoded.Value, `{"languages":["en"],"license":"None","release_time":"1783777307","source":{"hash":"727c8f4f681de1cee70903ccfbef38dac5d39104e247ec4d7cc597fdafc84fd1d8f89333207d01c16280e1bd4380dd05","media_type":"video/mp4","name":"RDT_20260711_103125.mp4","sd_hash":"36d72ae4e3d594fe090a17e881f53fd2a1acde20dcb64cc495b72c2f1a0f2cd838517b3eb21b54132367e68e4d601a58","size":"201549988"},"stream_type":"video","tags":["asmr","ear licking","ear eating","c:disable-comments","disable-support"],"thumbnail":{"url":"https://thumbs.odycdn.com/cc7adb516680e14604a19db0adf9beba.webp"},"title":"T4NG3R1N ASMR #03","video":{"duration":723,"height":718,"width":1280}}`)
	if !reflect.DeepEqual(decoded.Canonical, payload) {
		t.Fatalf("live canonical claim = %x, want %x", decoded.Canonical, payload)
	}
	marshaled, err := decoded.MarshalBinary()
	if err != nil || !reflect.DeepEqual(marshaled, payload) {
		t.Fatalf("MarshalBinary = %x, %v", marshaled, err)
	}
	marshaled[0] = 1
	if decoded.Canonical[0] != 0 {
		t.Fatal("MarshalBinary exposed the canonical backing slice")
	}
}

func TestDecodeClaimValueMatchesPinnedPythonTypeProjections(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		typeName string
		value    string
	}{
		{
			"stream", "000a5c0a360a02000112096d6f7669652e6d70341881808080808080102209766964656f2f6d70342a0968747470733a2f2f78320202033a02040528ffffffffffffffffff0132090803120300010218015a0c08800f10b80818017a02080242055469746c65520a0a01aa2a057468756d625a036f6e655a0374776f62070801105818ec016205100518fa016a0e08fa011205737461746528023001",
			"stream", `{"fee":{"address":"15T","amount":"0.01","currency":"USD"},"languages":["en-Latn-US","Arab-001"],"locations":[{"country":"R001","latitude":"1E-7","longitude":"-1E-7","state":"state"}],"release_time":"-1","source":{"bt_infohash":"BAU=","hash":"0001","media_type":"video/mp4","name":"movie.mp4","sd_hash":"0203","size":"9007199254740993","url":"https://x"},"stream_type":"video","tags":["one","two"],"thumbnail":{"hash":"qg==","url":"thumb"},"title":"Title","video":{"audio":{"duration":2},"duration":1,"height":1080,"width":1920}}`,
		},
		{
			"channel", "00124a0a2102111111111111111111111111111111111111111111111111111111111111111112036140622a20080212160a14000102030405060708090a0b0c0d0e0f1011121312040a02aabb42074368616e6e656c",
			"channel", `{"email":"a@b","featured":["131211100f0e0d0c0b0a09080706050403020100","bbaa"],"public_key":"021111111111111111111111111111111111111111111111111111111111111111","title":"Channel"}`,
		},
		{
			"collection", "001a20080212040a02010212160a1400000000000000000000000000000000000000004a0a436f6c6c656374696f6e",
			"collection", `{"claims":["0201","0000000000000000000000000000000000000000"],"description":"Collection","list_type":"DERIVATION"}`,
		},
		{
			"repost", "0022160a14000102030405060708090a0b0c0d0e0f1011121342065265706f7374",
			"repost", `{"claim_id":"131211100f0e0d0c0b0a09080706050403020100","title":"Repost"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := mustClaimValueHex(t, test.payload)
			decoded, err := DecodeClaimValue(payload)
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Type != test.typeName {
				t.Fatalf("claim type = %q, want %q", decoded.Type, test.typeName)
			}
			assertClaimValueJSON(t, decoded.Value, test.value)
			if !reflect.DeepEqual(decoded.Canonical, payload) {
				t.Fatalf("canonical claim = %x, want %x", decoded.Canonical, payload)
			}
		})
	}
}

func TestDecodeClaimValueSignedEnvelope(t *testing.T) {
	payload := mustClaimValueHex(t, "01000102030405060708090a0b0c0d0e0f10111213000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f0a0042065369676e6564")
	decoded, err := DecodeClaimValue(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.IsSigned() || len(decoded.SigningChannelHash) != 20 || len(decoded.Signature) != 64 {
		t.Fatalf("signed envelope = %#v", decoded)
	}
	channelID := decoded.SigningChannelID()
	if channelID == nil || *channelID != "131211100f0e0d0c0b0a09080706050403020100" {
		t.Fatalf("signing channel ID = %#v", channelID)
	}
	assertClaimValueJSON(t, decoded.Value, `{"title":"Signed"}`)
	if !reflect.DeepEqual(decoded.Canonical, payload) {
		t.Fatalf("signed canonical claim = %x, want %x", decoded.Canonical, payload)
	}

	var unmarshaled ClaimValue
	if err := unmarshaled.UnmarshalBinary(payload); err != nil || unmarshaled.Type != "stream" || !unmarshaled.IsSigned() {
		t.Fatalf("UnmarshalBinary = %#v, %v", unmarshaled, err)
	}
}

func TestDecodeClaimValuePreservesUnknownFields(t *testing.T) {
	payload := mustClaimValueHex(t, "00f806010a00")
	decoded, err := DecodeClaimValue(payload)
	if err != nil {
		t.Fatal(err)
	}
	wantCanonical := mustClaimValueHex(t, "000a00f80601")
	if decoded.Type != "stream" || len(decoded.Value) != 0 || !reflect.DeepEqual(decoded.Canonical, wantCanonical) {
		t.Fatalf("unknown-field claim = %#v", decoded)
	}
}

func TestDecodeClaimValueErrorBoundaries(t *testing.T) {
	if decoded, err := DecodeClaimValue(nil); decoded != nil || !errors.Is(err, ErrInvalidClaimValue) {
		t.Fatalf("empty claim = %#v, %v", decoded, err)
	} else if named, ok := err.(interface{ PythonErrorName() string }); !ok || named.PythonErrorName() != "IndexError" {
		t.Fatalf("empty Python error = %T %v", err, err)
	}
	for _, payload := range [][]byte{[]byte("{}"), {2, 0}} {
		if decoded, err := DecodeClaimValue(payload); decoded != nil || !errors.Is(err, ErrUnsupportedLegacyClaimValue) {
			t.Fatalf("legacy claim %x = %#v, %v", payload, decoded, err)
		}
	}
	if decoded, err := DecodeClaimValue([]byte{0, 0x0a}); decoded != nil || !errors.Is(err, ErrInvalidClaimValue) {
		t.Fatalf("malformed v2 claim = %#v, %v", decoded, err)
	} else if named, ok := err.(interface{ PythonErrorName() string }); !ok || named.PythonErrorName() != "DecodeError" {
		t.Fatalf("malformed Python error = %T %v", err, err)
	}
	if decoded, err := DecodeClaimValue([]byte{0}); decoded != nil ||
		!errors.Is(err, ErrInvalidClaimValue) ||
		transactionWirePythonErrorName(err) != "TypeError" ||
		err.Error() != "attribute name must be string, not 'NoneType'" {
		t.Fatalf("typeless v2 claim = %#v, %v", decoded, err)
	}
	if _, err := (*ClaimValue)(nil).MarshalBinary(); !errors.Is(err, ErrInvalidClaimValue) {
		t.Fatalf("nil MarshalBinary error = %v", err)
	}
}

func mustClaimValueHex(t *testing.T, encoded string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func assertClaimValueJSON(t *testing.T, value map[string]any, want string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != want {
		t.Fatalf("claim value JSON =\n%s\nwant\n%s", encoded, want)
	}
}
