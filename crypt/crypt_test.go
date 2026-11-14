package crypt

import "testing"

var testIV = []byte("0123456789abcdef")

func TestRoundTrip(t *testing.T) {
	for _, value := range []string{"a", "exactly16bytes!!", "a longer value that spans several blocks"} {
		out, err := Decrypt("pw", Encrypt("pw", value, testIV))
		if err != nil || out != value {
			t.Fatalf("round trip of %q gave %q, %v", value, out, err)
		}
	}
}

// Produced by lbry-sdk's lbry.crypto.crypt.aes_encrypt("hunter2", "seed words", iv=b"0123456789abcdef").
func TestPythonVector(t *testing.T) {
	out, err := Decrypt("hunter2", "MDEyMzQ1Njc4OWFiY2RlZoNseIwLAkeUPKSB6Wmt4vU=")
	if err != nil || out != "seed words" {
		t.Fatalf("got %q, %v", out, err)
	}
	if Encrypt("hunter2", "seed words", testIV) != "MDEyMzQ1Njc4OWFiY2RlZoNseIwLAkeUPKSB6Wmt4vU=" {
		t.Fatal("Encrypt does not match the Python output")
	}
}

func TestWrongPassword(t *testing.T) {
	if _, err := Decrypt("wrong", Encrypt("right", "secret value", testIV)); err == nil {
		t.Fatal("expected an error for the wrong password")
	}
}
