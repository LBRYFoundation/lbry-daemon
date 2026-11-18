package wallet

import (
	"testing"

	"lbry/daemon/crypto"
)

func TestAccountDecrypt(t *testing.T) {
	encrypted := func() Account {
		return Account{
			Encrypted:  true,
			Seed:       crypto.Encrypt("pw", "seed words", testIV),
			PrivateKey: crypto.Encrypt("pw", "xprv-key", testIV),
		}
	}

	account := encrypted()
	if err := account.Decrypt("pw"); err != nil || account.Encrypted || account.Seed != "seed words" || account.PrivateKey != "xprv-key" {
		t.Fatalf("right password: %+v %v", account, err)
	}

	// Each part that does not decrypt is an error, and the account is left as it was.
	wrongSeed := encrypted()
	wrongSeed.Seed = crypto.Encrypt("other", "seed words", testIV)
	// The private key alone does not decrypt: the seed does, so only the second check can catch it.
	wrongKey := encrypted()
	wrongKey.PrivateKey = crypto.Encrypt("other", "xprv-key", testIV)
	for name, account := range map[string]Account{"seed": wrongSeed, "private key": wrongKey} {
		before := account
		if err := account.Decrypt("pw"); err == nil {
			t.Errorf("a %s that does not decrypt must be an error", name)
		}
		if account.Encrypted != before.Encrypted || account.Seed != before.Seed || account.PrivateKey != before.PrivateKey {
			t.Errorf("a failed decrypt of the %s changed the account: %+v", name, account)
		}
	}

	plain := Account{Seed: "seed words"}
	if err := plain.Decrypt("pw"); err == nil {
		t.Error("decrypting an account that is not encrypted must be an error")
	}
}
