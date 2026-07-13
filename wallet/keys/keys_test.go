package keys

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
)

const (
	fixedMnemonicSeed = "919455c9f65198c3b0f8a2a656f13bd0ecc436abfabcb6a2a1f063affbccb628" +
		"230200066117a30b1aa3aec2800ddbd3bf405f088dd7c98ba4f25f58d47e1baf"
	fixedRootXPrv = "xprv9s21ZrQH143K42ovpZygnjfHdAqSd9jo7zceDfPRogM7bkkoNVv7DRNLEoB8" +
		"HoirMgH969NrgL8jNzLEegqFzPRWM37GXd4uE8uuRkx4LAe"
	fixedRootXPub = "xpub661MyMwAqRbcGWtPvbWh9sc2BCfw2cTeVDYF23o3N1t6UZ5wv3EMmDgp66FxH" +
		"uDtWdft3B5eL5xQtyzAtkdmhhC95gjRjLzSTdkho95asu9"
	fixedReceiveZeroXPrv = "xprv9vwXVierUTT4hmoe3dtTeBfbNv1ph2mm8RWXARU6HsZjBaAoFaS2FRQu4fptR" +
		"AyJWhJW42dmsEaC1nKnVKKTMhq3TVEHsNj1ca3ciZMKktT"
	fixedReceiveZeroXPub = "xpub69vsuEBkJq1MvFt79fRU1KcKvwrK6VVcVeS7xoshrD6i4NVwo7kGoDjNuw59" +
		"mk44XCPVQmbkGTc1i7ruD1bMf8aiyNiALZQqjK8BDvu8431"
)

func TestNetworkParametersMatchPinnedLedgers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		network      Network
		id           string
		pubkeyPrefix byte
		scriptPrefix byte
		xpub         string
		xprv         string
	}{
		{MainNet, "lbc_mainnet", 0x55, 0x7a, "0488b21e", "0488ade4"},
		{TestNet, "lbc_testnet", 0x6f, 0xc4, "043587cf", "04358394"},
		{RegTest, "lbc_regtest", 0x6f, 0xc4, "043587cf", "04358394"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.id, func(t *testing.T) {
			t.Parallel()
			if testCase.network.ID() != testCase.id {
				t.Fatalf("ID = %q, want %q", testCase.network.ID(), testCase.id)
			}
			if testCase.network.PubKeyAddressPrefix() != testCase.pubkeyPrefix {
				t.Fatalf("P2PKH prefix = %x, want %x", testCase.network.PubKeyAddressPrefix(), testCase.pubkeyPrefix)
			}
			if testCase.network.ScriptAddressPrefix() != testCase.scriptPrefix {
				t.Fatalf("P2SH prefix = %x, want %x", testCase.network.ScriptAddressPrefix(), testCase.scriptPrefix)
			}
			publicPrefix := testCase.network.ExtendedPublicKeyPrefix()
			if got := hex.EncodeToString(publicPrefix[:]); got != testCase.xpub {
				t.Fatalf("xpub prefix = %s, want %s", got, testCase.xpub)
			}
			privatePrefix := testCase.network.ExtendedPrivateKeyPrefix()
			if got := hex.EncodeToString(privatePrefix[:]); got != testCase.xprv {
				t.Fatalf("xprv prefix = %s, want %s", got, testCase.xprv)
			}
			if testCase.network.SecretPrefix() != 0x1c {
				t.Fatalf("secret prefix = %x, want 1c", testCase.network.SecretPrefix())
			}
			parsed, err := ParseNetwork(testCase.id)
			if err != nil || parsed != testCase.network {
				t.Fatalf("ParseNetwork(%q) = %v, %v", testCase.id, parsed, err)
			}
		})
	}
	if _, err := ParseNetwork("btc_mainnet"); !errors.Is(err, ErrUnknownNetwork) {
		t.Fatalf("unknown network error = %v", err)
	}
}

func TestSeedRootAndReceiveDerivationMatchPinnedSDK(t *testing.T) {
	t.Parallel()
	root, err := PrivateKeyFromSeed(MainNet, mustHex(t, fixedMnemonicSeed))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "root private bytes", hex.EncodeToString(root.PrivateKeyBytes()),
		"ef6c80310b1bcbcfa3176ea809ac840f48cda634c475d402e6bd68d5bb3827d6")
	rootChainCode := root.ChainCode()
	assertEqual(t, "root chain code", hex.EncodeToString(rootChainCode[:]),
		"c6342aaed881df5c493950956f178bc1201be02837da5525c4d3074e361f5f11")
	assertEqual(t, "root xprv", root.ExtendedKeyString(), fixedRootXPrv)
	assertEqual(t, "root xpub", root.PublicKey().ExtendedKeyString(), fixedRootXPub)
	assertEqual(t, "root address", root.Address(), "bbmkLZJvGdu6WFaRCZjZBgvagbRWjr5Xew")
	assertEqual(t, "legacy WIF bytes", hex.EncodeToString(root.LegacyWIFBytes()),
		"1cef6c80310b1bcbcfa3176ea809ac840f48cda634c475d402e6bd68d5bb3827d601")

	receive, err := root.Child(0)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "receive-chain xprv", receive.ExtendedKeyString(),
		"xprv9vkuCFwnpcZuSvjLPM29S1s2NJb4oxTjkX4XrCDn13SF4He7upnLrUZS3BMZ6hxYMDfuc4iAtJKcYMUj8gEMmHzy87SRJibQTKhvwH9vzm6")
	receiveZero, err := receive.Child(0)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "receive-zero xprv", receiveZero.ExtendedKeyString(), fixedReceiveZeroXPrv)
	assertEqual(t, "receive-zero xpub", receiveZero.PublicKey().ExtendedKeyString(), fixedReceiveZeroXPub)
	assertEqual(t, "receive-zero address", receiveZero.Address(), "bCqJrLHdoiRqEZ1whFZ3WHNb33bP34SuGx")
	if receiveZero.Depth() != 2 || receiveZero.ChildNumber() != 0 || receiveZero.Parent() != receive {
		t.Fatalf("receive-zero metadata = depth %d, child %d, parent %p", receiveZero.Depth(), receiveZero.ChildNumber(), receiveZero.Parent())
	}

	publicReceive, err := root.PublicKey().Child(0)
	if err != nil {
		t.Fatal(err)
	}
	publicReceiveZero, err := publicReceive.Child(0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(publicReceiveZero.CompressedBytes(), receiveZero.PublicKey().CompressedBytes()) {
		t.Fatal("public and private receive derivations produced different public keys")
	}
	assertEqual(t, "public receive-zero xpub", publicReceiveZero.ExtendedKeyString(), fixedReceiveZeroXPub)
}

func TestPrivateAndPublicChildVectorsMatchPinnedSDK(t *testing.T) {
	t.Parallel()
	chainCode := bytes.Repeat([]byte("abcd"), 8)
	privateKey, err := NewPrivateKey(
		MainNet,
		mustHex(t, "2423f3dc6087d9683f73a684935abc0ccd8bc26370588f56653128c6a6f0bf7c"),
		chainCode, 0, 1, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	child, err := privateKey.Child(0)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "normal private child", hex.EncodeToString(child.PrivateKeyBytes()),
		"95557ee9a2bb7665e67e45246658b5c839f7dcd99b6ebc800eeebccd28bf134a")
	hardened, err := privateKey.Child(1<<31 + 1)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "hardened private child", hex.EncodeToString(hardened.PrivateKeyBytes()),
		"abdba45b0459e7804beb68edb899e58a5c2636bf67d096711904001406afbd4c")

	publicKey, err := NewPublicKey(
		MainNet,
		mustHex(t, "03d1a3dc8155673bc1e2214fa493ccc82d57961b66054af9b6b653ac28eeef3ffe"),
		chainCode, 0, 1, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	publicChild, err := publicKey.Child(0)
	if err != nil {
		t.Fatal(err)
	}
	publicChildIdentifier := publicChild.Identifier()
	assertEqual(t, "public child identifier", hex.EncodeToString(publicChildIdentifier[:]),
		"948adae2a128c0bd1fa238117fd0d9690961f26e")
}

// Vectors are from tests/unit/wallet/key_fixtures.py at the pinned SDK commit;
// that file's SHA-256 is
// 2a02cab4b4f6ec9f0d29f7c6c7106f75e826c13bf98335bff773911f10080a74.
func TestAllPinnedSequentialChildVectors(t *testing.T) {
	t.Parallel()
	chainCode := bytes.Repeat([]byte("abcd"), 8)
	publicKey, err := NewPublicKey(
		MainNet,
		mustHex(t, "03d1a3dc8155673bc1e2214fa493ccc82d57961b66054af9b6b653ac28eeef3ffe"),
		chainCode, 0, 1, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	expectedIDs := []string{
		"948adae2a128c0bd1fa238117fd0d9690961f26e",
		"cd9f4f2adde7de0a53ab6d326bb6a62b489876dd",
		"c479e02a74a809ffecff60255d1c14f4081a197a",
		"4bab2fb2c424f31f170b15ec53c4a596db9d6710",
		"689cb7c621f57b7c398e7e04ed9a5098ab8389e9",
		"75116d6a689a0f9b56fe7cfec9cbbd0e16814288",
		"2439f0993fb298497dd7f317b9737c356f664a86",
		"32f1cb4799008cf5496bb8cafdaf59d5dabec6af",
		"fa29aa536353904e9cc813b0cf18efcc09e5ad13",
		"37df34002f34d7875428a2977df19be3f4f40a31",
		"8c8a72b5d2747a3e7e05ed85110188769d5656c3",
		"e5c8ef10c5bdaa79c9a237a096f50df4dcac27f0",
		"4d5270dc100fba85974665c20cd0f95d4822e8d1",
		"e76b07da0cdd59915475cd310599544b9744fa34",
		"6f009bccf8be99707161abb279d8ccf8fd953721",
		"f32f08b722cc8607c3f7f192b4d5f13a74c85785",
		"46f4430a5c91b9b799e9be6b47ac7a749d8d9f30",
		"ebbf9850abe0aae2d09e7e3ebd6b51f01282f39b",
		"5f6655438f8ddc6b2f6ea8197c8babaffc9f5c09",
		"e194e70ee8711b0ed765608121e4cceb551cdf28",
	}
	for index, want := range expectedIDs {
		child, err := publicKey.Child(int64(index))
		if err != nil {
			t.Fatalf("public child %d: %v", index, err)
		}
		identifier := child.Identifier()
		assertEqual(t, fmt.Sprintf("public child %d", index), hex.EncodeToString(identifier[:]), want)
	}

	privateKey, err := NewPrivateKey(
		MainNet,
		mustHex(t, "2423f3dc6087d9683f73a684935abc0ccd8bc26370588f56653128c6a6f0bf7c"),
		chainCode, 0, 1, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	expectedPrivate := []string{
		"95557ee9a2bb7665e67e45246658b5c839f7dcd99b6ebc800eeebccd28bf134a",
		"689b6921f65647a8e4fc1497924730c92ad4ad183f10fac2bdee65cc8fb6dcf9",
		"977ee018b448c530327b7e927cc3645ca4cb152c5dd98e1bd917c52fd46fc80a",
		"3c7fb05b0ab4da8b292e895f574f8213cadfe81b84ded7423eab61c5f884c8ae",
		"b21fc7be1e69182827538683a48ac9d95684faf6c1c6deabb6e513d8c76afcc9",
		"a5021734dbbf1d090b15509ba00f2c04a3d5afc19939b4594ca0850d4190b923",
		"07dfe0aa94c1b948dc935be1f8179f3050353b46f3a3134e77c70e66208be72d",
		"c331b2fb82cd91120b0703ee312042a854a51a8d945aa9e70fb14d68b0366fe1",
		"3aa59ec4d8f1e7ce2775854b5e82433535b6e3503f9a8e7c4e60aac066d44718",
		"ccc8b4ca73b266b4a0c89a9d33c4ec7532b434c9294c26832355e5e2bee2e005",
		"280c074d8982e56d70c404072252c309694a6e5c05457a6abbe8fc225c2dfd52",
		"546cee26da713a3a64b2066d5e3a52b7c1d927396d1ba8a3d9f6e3e973398856",
		"7fbc4615d5e819eee22db440c5bcc4ff25bb046841c41a192003a6d9abfbafbf",
		"5b63f13011cab965feea3a41fac2d7a877aa710ab20e2a9a1708474e3c05c050",
		"394b36f528947557d317fd40a4adde5514c8745a5f64185421fa2c0c4a158938",
		"8f101c8f5290ae6c0dd76d210b7effacd7f12db18f3befab711f533bde084c76",
		"6637a656f897a66080fbe60027d32c3f4ebc0e3b5f96123a33f932a091b039c2",
		"2815aa6667c042a3a4565fb789890cd33e380d047ed712759d097d479df71051",
		"120e761c6382b07a9548650a20b3b9dd74b906093260fa6f92f790ba71f79e8d",
		"823c8a613ea539f730a968518993195174bf973ed75c734b6898022867165d7b",
	}
	for index, want := range expectedPrivate {
		child, err := privateKey.Child(int64(index))
		if err != nil {
			t.Fatalf("private child %d: %v", index, err)
		}
		assertEqual(t, fmt.Sprintf("private child %d", index), hex.EncodeToString(child.PrivateKeyBytes()), want)
	}

	expectedHardened := []string{
		"abdba45b0459e7804beb68edb899e58a5c2636bf67d096711904001406afbd4c",
		"c9e804d4b8fdd99ef6ab2b0ca627a57f4283c28e11e9152ad9d3f863404d940e",
		"4cf87d68ae99711261f8cb8e1bde83b8703ff5d689ef70ce23106d1e6e8ed4bd",
		"dbf8d578c77f9bf62bb2ad40975e253af1e1d44d53abf84a22d2be29b9488f7f",
		"633bb840505521ffe39cb89a04fb8bff3298d6b64a5d8f170aca1e456d6f89b9",
		"92e80a38791bd8ba2105b9867fd58ac2cc4fb9853e18141b7fee1884bc5aae69",
		"d3663339af1386d05dd90ee20f627661ae87ddb1db0c2dc73fd8a4485930d0e7",
		"09a448303452d241b8a25670b36cc758975b97e88f62b6f25cd9084535e3c13a",
		"ee22eb77df05ff53e9c2ba797c1f2ebf97ec4cf5a99528adec94972674aeabed",
		"935facccb6120659c5b7c606a457c797e5a10ce4a728346e1a3a963251169651",
		"8ac9b4a48da1def375640ca03bc6711040dfd4eea7106d42bb4c2de83d7f595e",
		"51ecd3f7565c2b86d5782dbde2175ab26a7b896022564063fafe153588610be9",
		"04918252f6b6f51cd75957289b56a324b45cc085df80839137d740f9ada6c062",
		"2efbd0c839af971e3769c26938d776990ebf097989df4861535a7547a2701483",
		"85c6e31e6b27bd188291a910f4a7faba7fceb3e09df72884b10907ecc1491cd0",
		"05e245131885bebda993a31bb14ac98b794062a50af639ad22010aed1e533a54",
		"ddca42cf7db93f3a3f0723d5fee4c21bf60b7afac35d5c30eb34bd91b35cc609",
		"324a5c16030e0c3947e4dcd2b5057fd3a4d5bed96b23e3b476b2af0ab76369c9",
		"da63c41cdb398cdcd93e832f3e198528afbb4065821b026c143cec910d8362f0",
	}
	for offset, want := range expectedHardened {
		index := int64(1<<31 + 1 + offset)
		child, err := privateKey.Child(index)
		if err != nil {
			t.Fatalf("hardened private child %d: %v", index, err)
		}
		assertEqual(t, fmt.Sprintf("hardened private child %d", index), hex.EncodeToString(child.PrivateKeyBytes()), want)
	}
}

func TestTestnetAndRegtestEncodingMatchPinnedLedgers(t *testing.T) {
	t.Parallel()
	for _, network := range []Network{TestNet, RegTest} {
		root, err := PrivateKeyFromSeed(network, mustHex(t, fixedMnemonicSeed))
		if err != nil {
			t.Fatal(err)
		}
		assertEqual(t, network.ID()+" tprv", root.ExtendedKeyString(),
			"tprv8ZgxMBicQKsPer3TV8qBxPHGwJFerfmoTYXm65otHeqbPMVtMsFrjAjn9yLnJB7Aj7ov6EzcqgiXqqsymuBCoSh6sgKaBynx9EfKsUrkV7y")
		assertEqual(t, network.ID()+" tpub", root.PublicKey().ExtendedKeyString(),
			"tpubD6NzVbkrYhZ4YK5FNnVnMnwPWKmb1zxi2r8YNbrBhvdzDqkezG5SufMeL8LjpQB8JYCnekbWsBnTbAUnHcV1b8eS9HUjQseg3bM7Z7PwX35")
		assertEqual(t, network.ID()+" address", root.Address(), "n4ZRwP4QjKwsmXCfqUPqnx133i83Ha7GbW")
	}
}

func TestParsedChildExtendedKeyLosesParentFingerprint(t *testing.T) {
	t.Parallel()
	extended, err := ParseExtendedKey(MainNet, fixedReceiveZeroXPub)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, ok := extended.(*PublicKey)
	if !ok {
		t.Fatalf("parsed key type = %T, want *PublicKey", extended)
	}
	if publicKey.Parent() != nil || publicKey.Depth() != 2 {
		t.Fatalf("parsed parent/depth = %p/%d", publicKey.Parent(), publicKey.Depth())
	}
	assertEqual(t, "parsed child reserialization", publicKey.ExtendedKeyString(),
		"xpub69mdgvyDG2wbxcxP2Z5QhcxfZZqGZV62SfiRS7bF8gKy9bbBUfCUz5knwkjAZSM7a6AzvMMd6EKpZKM3FxQnR1cHfwNEy221Yhq7h6c9hTX")
	parsedPrivate, err := ParseExtendedKey(MainNet, fixedReceiveZeroXPrv)
	if err != nil {
		t.Fatal(err)
	}
	privateChild, ok := parsedPrivate.(*PrivateKey)
	if !ok {
		t.Fatalf("parsed key type = %T, want *PrivateKey", parsedPrivate)
	}
	assertEqual(t, "parsed private child reserialization", privateChild.ExtendedKeyString(),
		"xprv9vnHHRSKRfPJk8suvXYQLV1w1XznA2NB5SnpdjBdaLnzGoG2w7tESHSK6VUuCsGMZb61ZcPeh1HzryovYG8t7arcA3tNVqLBRxkZBe3Gcrd")

	parsedRoot, err := ParseExtendedKey(MainNet, fixedRootXPrv)
	if err != nil {
		t.Fatal(err)
	}
	privateRoot, ok := parsedRoot.(*PrivateKey)
	if !ok {
		t.Fatalf("parsed root type = %T, want *PrivateKey", parsedRoot)
	}
	assertEqual(t, "root round trip", privateRoot.ExtendedKeyString(), fixedRootXPrv)
}

func TestKeyValidationRejectsPythonInvalidInputs(t *testing.T) {
	t.Parallel()
	validSecret := make([]byte, 32)
	validSecret[31] = 1
	validChain := make([]byte, 32)
	validPublic := secp256k1.PrivKeyFromBytes(validSecret).PubKey().SerializeCompressed()
	order := mustHex(t, "fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141")

	privateCases := []struct {
		name    string
		network Network
		secret  []byte
		chain   []byte
		child   int64
		depth   int
		want    error
	}{
		{"unknown network", Network(99), validSecret, validChain, 0, 0, ErrUnknownNetwork},
		{"short chain", MainNet, validSecret, validChain[:31], 0, 0, ErrInvalidChainCode},
		{"negative child", MainNet, validSecret, validChain, -1, 0, ErrInvalidChildNumber},
		{"large child", MainNet, validSecret, validChain, 1 << 32, 0, ErrInvalidChildNumber},
		{"negative depth", MainNet, validSecret, validChain, 0, -1, ErrInvalidDepth},
		{"large depth", MainNet, validSecret, validChain, 0, 256, ErrInvalidDepth},
		{"short scalar", MainNet, validSecret[:31], validChain, 0, 0, ErrInvalidPrivateKey},
		{"zero scalar", MainNet, make([]byte, 32), validChain, 0, 0, ErrInvalidPrivateKey},
		{"order scalar", MainNet, order, validChain, 0, 0, ErrInvalidPrivateKey},
	}
	for _, testCase := range privateCases {
		testCase := testCase
		t.Run("private/"+testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewPrivateKey(testCase.network, testCase.secret, testCase.chain, testCase.child, testCase.depth, nil)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
		})
	}

	publicCases := []struct {
		name   string
		public []byte
		want   error
	}{
		{"short point", validPublic[:32], ErrInvalidPublicKey},
		{"uncompressed point", secp256k1.PrivKeyFromBytes(validSecret).PubKey().SerializeUncompressed(), ErrInvalidPublicKey},
		{"invalid prefix", append([]byte{0x04}, validPublic[1:]...), ErrInvalidPublicKey},
		{"invalid point", append([]byte{0x02}, bytes.Repeat([]byte{0xff}, 32)...), ErrInvalidPublicKey},
	}
	for _, testCase := range publicCases {
		testCase := testCase
		t.Run("public/"+testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewPublicKey(MainNet, testCase.public, validChain, 0, 0, nil)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestChildBoundsAndInvalidTweaksAreRejected(t *testing.T) {
	t.Parallel()
	secret := make([]byte, 32)
	secret[31] = 1
	privateKey, err := NewPrivateKey(MainNet, secret, make([]byte, 32), 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []int64{-1, 1 << 32} {
		if _, err := privateKey.Child(index); !errors.Is(err, ErrInvalidChildNumber) {
			t.Fatalf("private Child(%d) error = %v", index, err)
		}
	}
	for _, index := range []int64{-1, 1 << 32} {
		if _, err := privateKey.PublicKey().Child(index); !errors.Is(err, ErrInvalidChildNumber) {
			t.Fatalf("public Child(%d) error = %v", index, err)
		}
	}
	if _, err := privateKey.PublicKey().Child(1 << 31); !errors.Is(err, ErrInvalidChildNumber) {
		t.Fatalf("hardened public child error = %v", err)
	}

	depthLimit, err := NewPrivateKey(MainNet, secret, make([]byte, 32), 0, 255, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := depthLimit.Child(0); !errors.Is(err, ErrInvalidDepth) {
		t.Fatalf("depth-limit child error = %v", err)
	}

	order := mustHex(t, "fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141")
	orderMinusOne := mustHex(t, "fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364140")
	for name, tweak := range map[string][]byte{"order": order, "zero result": orderMinusOne} {
		if _, err := addPrivateTweak(privateKey.key, tweak); !errors.Is(err, ErrInvalidDerivation) {
			t.Fatalf("private %s tweak error = %v", name, err)
		}
		if _, err := addPublicTweak(privateKey.PublicKey().key, tweak); !errors.Is(err, ErrInvalidDerivation) {
			t.Fatalf("public %s tweak error = %v", name, err)
		}
	}
	zero := make([]byte, 32)
	zeroPrivate, err := addPrivateTweak(privateKey.key, zero)
	if err != nil {
		t.Fatalf("zero private tweak error = %v", err)
	}
	if !bytes.Equal(zeroPrivate.Serialize(), privateKey.PrivateKeyBytes()) {
		t.Fatal("zero private tweak changed the key")
	}
	zeroPublic, err := addPublicTweak(privateKey.PublicKey().key, zero)
	if err != nil {
		t.Fatalf("zero public tweak error = %v", err)
	}
	if !bytes.Equal(zeroPublic.SerializeCompressed(), privateKey.PublicKey().CompressedBytes()) {
		t.Fatal("zero public tweak changed the key")
	}
}

func TestExtendedKeyParsingRejectsMalformedValues(t *testing.T) {
	t.Parallel()
	if _, err := ParseExtendedKey(MainNet, fixedRootXPub[:len(fixedRootXPub)-1]+"0"); !errors.Is(err, ErrInvalidBase58Character) {
		t.Fatalf("invalid character error = %v", err)
	}
	corrupted := fixedRootXPub[:len(fixedRootXPub)-1] + "1"
	if _, err := ParseExtendedKey(MainNet, corrupted); !errors.Is(err, ErrInvalidBase58Checksum) {
		t.Fatalf("invalid checksum error = %v", err)
	}
	if _, err := ParseExtendedKey(TestNet, fixedRootXPub); !errors.Is(err, ErrUnknownKeyVersion) {
		t.Fatalf("wrong-network version error = %v", err)
	}

	raw, err := DecodeBase58Check(fixedRootXPrv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseExtendedKeyBytes(MainNet, raw[:77]); !errors.Is(err, ErrInvalidExtendedKey) {
		t.Fatalf("short extended key error = %v", err)
	}
	badPrivatePrefix := append([]byte(nil), raw...)
	badPrivatePrefix[45] = 1
	if _, err := ParseExtendedKeyBytes(MainNet, badPrivatePrefix); !errors.Is(err, ErrInvalidExtendedKey) {
		t.Fatalf("bad private prefix error = %v", err)
	}
	unknownVersion := append([]byte(nil), raw...)
	copy(unknownVersion[:4], []byte{1, 2, 3, 4})
	if _, err := ParseExtendedKeyBytes(MainNet, unknownVersion); !errors.Is(err, ErrUnknownKeyVersion) {
		t.Fatalf("unknown version error = %v", err)
	}
	zeroPrivate := append([]byte(nil), raw...)
	clear(zeroPrivate[46:])
	if _, err := ParseExtendedKeyBytes(MainNet, zeroPrivate); !errors.Is(err, ErrInvalidPrivateKey) {
		t.Fatalf("zero extended private key error = %v", err)
	}

	publicRaw, err := DecodeBase58Check(fixedRootXPub)
	if err != nil {
		t.Fatal(err)
	}
	invalidPoint := append([]byte(nil), publicRaw...)
	invalidPoint[45] = 0x02
	for index := 46; index < len(invalidPoint); index++ {
		invalidPoint[index] = 0xff
	}
	if _, err := ParseExtendedKeyBytes(MainNet, invalidPoint); !errors.Is(err, ErrInvalidPublicKey) {
		t.Fatalf("invalid extended public point error = %v", err)
	}
}

func TestBase58CheckAndHash160(t *testing.T) {
	t.Parallel()
	payload := mustHex(t, "55fcc2ccf6d6e5aa4f02f897e4889e0bfcf22d8196")
	encoded := EncodeBase58Check(payload)
	assertEqual(t, "base58check", encoded, "bbmkLZJvGdu6WFaRCZjZBgvagbRWjr5Xew")
	decoded, err := DecodeBase58Check(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("decoded payload = %x, want %x", decoded, payload)
	}
	publicKey := mustHex(t, "03fdc469f547fd799210e8a4602eb394466b1d4935146a7da2bbeb4880b223d904")
	identifier := Hash160(publicKey)
	assertEqual(t, "hash160", hex.EncodeToString(identifier[:]), "fcc2ccf6d6e5aa4f02f897e4889e0bfcf22d8196")
}

func TestBase58CheckPreservesLeadingZeroes(t *testing.T) {
	t.Parallel()
	for _, payload := range [][]byte{{0}, {0, 0, 1}, {0, 0, 0xff, 1}} {
		encoded := EncodeBase58Check(payload)
		decoded, err := DecodeBase58Check(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded, payload) {
			t.Fatalf("Base58Check round trip for %x = %x", payload, decoded)
		}
	}
}

func TestPermissiveBase58DecodeMatchesPinnedZeroQuirk(t *testing.T) {
	t.Parallel()
	for encoded, want := range map[string][]byte{
		"1":  {0, 0},
		"11": {0, 0, 0},
		"2":  {1},
	} {
		decoded, err := DecodeBase58(encoded)
		if err != nil || !bytes.Equal(decoded, want) {
			t.Fatalf("DecodeBase58(%q) = %x, %v, want %x", encoded, decoded, err, want)
		}
	}
}

func TestKeyInputsAndSerializedOutputsDoNotAlias(t *testing.T) {
	t.Parallel()
	secret := mustHex(t, "2423f3dc6087d9683f73a684935abc0ccd8bc26370588f56653128c6a6f0bf7c")
	chainCode := bytes.Repeat([]byte("abcd"), 8)
	secretSnapshot := append([]byte(nil), secret...)
	chainSnapshot := append([]byte(nil), chainCode...)
	key, err := NewPrivateKey(MainNet, secret, chainCode, 7, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	secret[0], chainCode[0] = 0, 0
	if !bytes.Equal(key.PrivateKeyBytes(), secretSnapshot) {
		t.Fatal("private key retained caller-owned secret storage")
	}
	storedChain := key.ChainCode()
	if !bytes.Equal(storedChain[:], chainSnapshot) {
		t.Fatal("key retained caller-owned chain-code storage")
	}

	first := key.ExtendedKeyBytes()
	first[0], first[45] = 0, 0xff
	second := key.ExtendedKeyBytes()
	if second[0] != 0x04 || second[45] != 0 {
		t.Fatal("ExtendedKeyBytes exposed internal storage")
	}
	privateBytes := key.PrivateKeyBytes()
	privateBytes[0] ^= 0xff
	if !bytes.Equal(key.PrivateKeyBytes(), secretSnapshot) {
		t.Fatal("PrivateKeyBytes exposed internal storage")
	}

	parsedBytes := key.ExtendedKeyBytes()
	parsed, err := ParseExtendedKeyBytes(MainNet, parsedBytes)
	if err != nil {
		t.Fatal(err)
	}
	want := parsed.ExtendedKeyString()
	clear(parsedBytes)
	if parsed.ExtendedKeyString() != want {
		t.Fatal("parsed extended key retained input storage")
	}
}

func mustHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func assertEqual(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %q, want %q", name, got, want)
	}
}
