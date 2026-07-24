package wallet

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/mnemonic"
)

const (
	DeterministicChainGenerator = "deterministic-chain"
	SingleAddressGenerator      = "single-address"
	ReceiveChain                = 0
	ChangeChain                 = 1
	ChannelChain                = 2
)

var (
	ErrInvalidAccountData         = errors.New("invalid account data")
	ErrUnknownAddressGenerator    = errors.New("unknown account address generator")
	ErrAddressGeneratorMismatch   = errors.New("account address generator mismatch")
	ErrAccountAlreadyEncrypted    = errors.New("account is already encrypted")
	ErrAccountNotEncrypted        = errors.New("account is not encrypted")
	ErrEncryptedAccountHash       = errors.New("cannot hash an encrypted account")
	ErrAccountPrivateKeyMissing   = errors.New("account has no private key")
	ErrEncryptedAccountPrivateKey = errors.New("cannot get private key from encrypted account")
)

type AccountOption func(*accountOptions)

type accountOptions struct {
	now     func() time.Time
	entropy io.Reader
}

// WithAccountClock controls the int(time.time()) fallback used for a missing
// modified_on value. It also preserves the obscure merge fallback when a
// negative local timestamp is compared with a missing incoming timestamp.
func WithAccountClock(clock func() time.Time) AccountOption {
	return func(options *accountOptions) {
		if clock != nil {
			options.now = clock
		}
	}
}

// WithAccountEntropy controls the lazy 16-byte IV source. It is primarily
// useful for compatibility fixtures and deterministic wallet migrations.
func WithAccountEntropy(entropy io.Reader) AccountOption {
	return func(options *accountOptions) {
		if entropy != nil {
			options.entropy = entropy
		}
	}
}

// AddressManager owns Python-compatible derivation settings and the lock used
// by the database-backed inventory helpers in address_manager.go.
type AddressManager struct {
	account               *Account
	PublicKey             *keys.PublicKey
	ChainNumber           int64
	Gap                   any
	MaximumUsesPerAddress any
	singleAddress         bool
	addressGeneratorMu    sync.Mutex
}

// GetPublicKey matches the key selection performed by the two pinned address
// manager types. A single-address manager intentionally ignores index.
func (manager *AddressManager) GetPublicKey(index int64) (*keys.PublicKey, error) {
	if manager == nil || manager.account == nil {
		return nil, fmt.Errorf("%w: address manager is not initialized", ErrInvalidAccountData)
	}
	if manager.singleAddress {
		return manager.account.PublicKey, nil
	}
	return manager.PublicKey.Child(index)
}

// GetPrivateKey is the isolated key derivation counterpart to GetPublicKey.
// Resolving an arbitrary persisted address back to its chain/index remains a
// later transaction and signing milestone.
func (manager *AddressManager) GetPrivateKey(index int64) (*keys.PrivateKey, error) {
	if manager == nil || manager.account == nil {
		return nil, fmt.Errorf("%w: address manager is not initialized", ErrInvalidAccountData)
	}
	// SingleKey returns account.private_key directly. Read-only and encrypted
	// accounts therefore return nil without trying to derive or unlock it.
	if manager.singleAddress {
		return manager.account.PrivateKey, nil
	}
	if manager.account.Encrypted {
		return nil, ErrEncryptedAccountPrivateKey
	}
	if manager.account.PrivateKey == nil {
		return nil, ErrAccountPrivateKeyMissing
	}
	chain, err := manager.account.PrivateKey.Child(manager.ChainNumber)
	if err != nil {
		return nil, err
	}
	return chain.Child(index)
}

// Account is the ordered state represented by lbry.wallet.account.Account.
// Its unexported ledger link supports database-backed channel-key priming and
// address inventories; wallet RPC exposure remains outside this type.
type Account struct {
	Network                  keys.Network
	ID                       string
	Name                     string
	Seed                     string
	ModifiedOn               *big.Int
	PrivateKeyString         string
	Encrypted                bool
	PrivateKey               *keys.PrivateKey
	PublicKey                *keys.PublicKey
	GeneratorName            string
	Receiving                *AddressManager
	Change                   *AddressManager
	ChannelKeys              *Object
	DeterministicChannelKeys *DeterministicChannelKeyManager

	initializationVectors map[string][]byte
	now                   func() time.Time
	entropy               io.Reader
	wallet                *Wallet
	ledger                *Ledger
}

type accountKeyMaterial struct {
	seed             string
	privateKeyString string
	encrypted        bool
	privateKey       *keys.PrivateKey
	publicKey        *keys.PublicKey
}

// NewAccount constructs an account from an insertion-ordered Python-style
// dictionary. The caller's certificates Object remains shared intentionally:
// Python stores that dictionary directly and later mutates it during merge.
func NewAccount(network keys.Network, data *Object, options ...AccountOption) (*Account, error) {
	if data == nil {
		return nil, fmt.Errorf("%w: account dictionary is nil", ErrInvalidAccountData)
	}
	settings := accountOptions{now: time.Now, entropy: rand.Reader}
	for _, option := range options {
		if option != nil {
			option(&settings)
		}
	}

	keyMaterial, err := accountKeysFromObject(network, data)
	if err != nil {
		return nil, err
	}

	name, err := accountString(data, "name", "")
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = "Account #" + keyMaterial.publicKey.Address()
	}
	modifiedValue, exists := data.Get("modified_on")
	var modifiedOn *big.Int
	if exists {
		modifiedOn, err = accountPythonInt(modifiedValue)
		if err != nil {
			return nil, fmt.Errorf("%w: modified_on: %v", ErrInvalidAccountData, err)
		}
	} else {
		modifiedOn = accountPythonTimeInt(settings.now())
	}

	channelKeys := NewObject()
	if value, exists := data.Get("certificates"); exists {
		var ok bool
		channelKeys, ok = value.(*Object)
		if !ok || channelKeys == nil {
			return nil, fmt.Errorf("%w: certificates has type %T, want object", ErrInvalidAccountData, value)
		}
	}
	account := &Account{
		Network:               network,
		ID:                    keyMaterial.publicKey.Address(),
		Name:                  name,
		Seed:                  keyMaterial.seed,
		ModifiedOn:            modifiedOn,
		PrivateKeyString:      keyMaterial.privateKeyString,
		Encrypted:             keyMaterial.encrypted,
		PrivateKey:            keyMaterial.privateKey,
		PublicKey:             keyMaterial.publicKey,
		ChannelKeys:           channelKeys,
		initializationVectors: make(map[string][]byte),
		now:                   settings.now,
		entropy:               settings.entropy,
	}
	if err := account.loadAddressGenerator(data); err != nil {
		return nil, err
	}
	account.DeterministicChannelKeys = NewDeterministicChannelKeyManager(account)
	return account, nil
}

// accountKeysFromObject is the isolated Account.keys_from_dict compatibility
// boundary. Wallet.Merge uses it to identify a record before validating name,
// timestamps, address-generator settings, or certificates.
func accountKeysFromObject(network keys.Network, data *Object) (accountKeyMaterial, error) {
	var material accountKeyMaterial
	if data == nil {
		return material, fmt.Errorf("%w: account dictionary is nil", ErrInvalidAccountData)
	}
	var err error
	material.seed, err = accountString(data, "seed", "")
	if err != nil {
		return material, err
	}
	material.privateKeyString, err = accountString(data, "private_key", "")
	if err != nil {
		return material, err
	}
	material.encrypted, err = accountBool(data, "encrypted", false)
	if err != nil {
		return material, err
	}

	if !material.encrypted {
		switch {
		case material.seed != "":
			// The empty password passed by Account.keys_from_dict becomes the
			// legacy default through `password or "lbryum"`.
			material.privateKey, err = keys.PrivateKeyFromSeed(
				network, mnemonic.ToSeed(material.seed, "lbryum"),
			)
			if err == nil {
				material.publicKey = material.privateKey.PublicKey()
			}
		case material.privateKeyString != "":
			var parsed keys.ExtendedKey
			parsed, err = keys.ParseExtendedKey(network, material.privateKeyString)
			if err == nil {
				var ok bool
				material.privateKey, ok = parsed.(*keys.PrivateKey)
				if !ok {
					err = fmt.Errorf("%w: private_key is not an extended private key", ErrInvalidAccountData)
				} else {
					material.publicKey = material.privateKey.PublicKey()
				}
			}
		}
		if err != nil {
			return material, fmt.Errorf("load account private key: %w", err)
		}
	}
	if material.publicKey == nil {
		publicKeyString, stringErr := accountRequiredString(data, "public_key")
		if stringErr != nil {
			return material, stringErr
		}
		parsed, parseErr := keys.ParseExtendedKey(network, publicKeyString)
		if parseErr != nil {
			return material, fmt.Errorf("load account public key: %w", parseErr)
		}
		var ok bool
		material.publicKey, ok = parsed.(*keys.PublicKey)
		if !ok {
			return material, fmt.Errorf("%w: public_key is not an extended public key", ErrInvalidAccountData)
		}
	}
	return material, nil
}

// accountIDFromObject applies keys_from_dict's lazy precedence for Merge.
// Unlike full account construction, unused malformed secret fields must not
// prevent an existing account match.
func accountIDFromObject(network keys.Network, data *Object) (string, error) {
	if data == nil {
		return "", fmt.Errorf("%w: account dictionary is nil", ErrInvalidAccountData)
	}
	encryptedValue, hasEncrypted := data.Get("encrypted")
	encrypted := hasEncrypted && pythonJSONTruthy(encryptedValue)
	if !encrypted {
		seedValue, hasSeed := data.Get("seed")
		if hasSeed && pythonJSONTruthy(seedValue) {
			seed, ok := seedValue.(string)
			if !ok {
				return "", fmt.Errorf("%w: seed has type %T, want string", ErrInvalidAccountData, seedValue)
			}
			privateKey, err := keys.PrivateKeyFromSeed(network, mnemonic.ToSeed(seed, "lbryum"))
			if err != nil {
				return "", fmt.Errorf("load account private key: %w", err)
			}
			return privateKey.PublicKey().Address(), nil
		}
		privateKeyValue, hasPrivateKey := data.Get("private_key")
		if hasPrivateKey && pythonJSONTruthy(privateKeyValue) {
			privateKeyString, ok := privateKeyValue.(string)
			if !ok {
				return "", fmt.Errorf("%w: private_key has type %T, want string", ErrInvalidAccountData, privateKeyValue)
			}
			parsed, err := keys.ParseExtendedKey(network, privateKeyString)
			if err != nil {
				return "", fmt.Errorf("load account private key: %w", err)
			}
			privateKey, ok := parsed.(*keys.PrivateKey)
			if !ok {
				return "", fmt.Errorf("%w: private_key is not an extended private key", ErrInvalidAccountData)
			}
			return privateKey.PublicKey().Address(), nil
		}
	}

	publicKeyValue, exists := data.Get("public_key")
	if !exists {
		return "", fmt.Errorf("%w: public_key is missing", ErrInvalidAccountData)
	}
	publicKeyString, ok := publicKeyValue.(string)
	if !ok {
		return "", fmt.Errorf("%w: public_key has type %T, want string", ErrInvalidAccountData, publicKeyValue)
	}
	parsed, err := keys.ParseExtendedKey(network, publicKeyString)
	if err != nil {
		return "", fmt.Errorf("load account public key: %w", err)
	}
	switch key := parsed.(type) {
	case *keys.PublicKey:
		return key.Address(), nil
	case *keys.PrivateKey:
		// Python accepts an xprv here and uses its public address. Full Go
		// account construction remains stricter for this malformed state.
		return key.Address(), nil
	default:
		return "", fmt.Errorf("%w: unsupported extended public key type %T", ErrInvalidAccountData, parsed)
	}
}

func (account *Account) loadAddressGenerator(data *Object) error {
	generator := NewObject()
	if value, exists := data.Get("address_generator"); exists {
		var ok bool
		generator, ok = value.(*Object)
		if !ok || generator == nil {
			return fmt.Errorf("%w: address_generator has type %T, want object", ErrInvalidAccountData, value)
		}
	}
	name := DeterministicChainGenerator
	if value, exists := generator.Get("name"); exists {
		var ok bool
		name, ok = value.(string)
		if !ok {
			return fmt.Errorf("%w: address_generator.name has type %T, want string", ErrInvalidAccountData, value)
		}
	}
	account.GeneratorName = name
	switch name {
	case DeterministicChainGenerator:
		receivingKey, err := account.PublicKey.Child(ReceiveChain)
		if err != nil {
			return fmt.Errorf("derive receiving chain: %w", err)
		}
		changeKey, err := account.PublicKey.Child(ChangeChain)
		if err != nil {
			return fmt.Errorf("derive change chain: %w", err)
		}
		receiving, err := loadDeterministicManager(account, generator, "receiving", ReceiveChain, receivingKey, 20)
		if err != nil {
			return err
		}
		change, err := loadDeterministicManager(account, generator, "change", ChangeChain, changeKey, 6)
		if err != nil {
			return err
		}
		account.Receiving, account.Change = receiving, change
	case SingleAddressGenerator:
		manager := &AddressManager{
			account:       account,
			PublicKey:     account.PublicKey,
			ChainNumber:   ReceiveChain,
			singleAddress: true,
		}
		account.Receiving, account.Change = manager, manager
	default:
		return fmt.Errorf("%w: %q", ErrUnknownAddressGenerator, name)
	}
	return nil
}

func loadDeterministicManager(
	account *Account, generator *Object, chainName string, chainNumber int64,
	publicKey *keys.PublicKey, defaultGap int,
) (*AddressManager, error) {
	gap := any(defaultGap)
	maximumUses := any(1)
	if value, exists := generator.Get(chainName); exists {
		chain, ok := value.(*Object)
		if !ok || chain == nil {
			return nil, fmt.Errorf("%w: address_generator.%s has type %T, want object", ErrInvalidAccountData, chainName, value)
		}
		var found bool
		gap, found = chain.Get("gap")
		if !found {
			return nil, fmt.Errorf("%w: address_generator.%s.gap is missing", ErrInvalidAccountData, chainName)
		}
		maximumUses, found = chain.Get("maximum_uses_per_address")
		if !found {
			return nil, fmt.Errorf("%w: address_generator.%s.maximum_uses_per_address is missing", ErrInvalidAccountData, chainName)
		}
		if chain.Len() != 2 {
			return nil, fmt.Errorf("%w: address_generator.%s has unexpected constructor fields", ErrInvalidAccountData, chainName)
		}
	}
	return &AddressManager{
		account:               account,
		PublicKey:             publicKey,
		ChainNumber:           chainNumber,
		Gap:                   gap,
		MaximumUsesPerAddress: maximumUses,
	}, nil
}

// ToObject is the no-encryption, include-certificates form of Python's
// Account.to_dict().
func (account *Account) ToObject() (*Object, error) {
	return account.ToDict("", true)
}

// ToDict returns the exact insertion order produced by Account.to_dict.
// A non-empty password performs transient serialization encryption and only
// mutates the account's cached IVs, not its lock state or secret fields.
func (account *Account) ToDict(encryptPassword string, includeCertificates bool) (*Object, error) {
	if account == nil || account.PublicKey == nil || account.ModifiedOn == nil {
		return nil, fmt.Errorf("%w: account is not initialized", ErrInvalidAccountData)
	}
	privateKeyString, seed := account.PrivateKeyString, account.Seed
	if !account.Encrypted && account.PrivateKey != nil {
		privateKeyString = account.PrivateKey.ExtendedKeyString()
	}
	if !account.Encrypted && encryptPassword != "" {
		var err error
		if privateKeyString != "" {
			privateKeyString, err = account.encryptForSerialization("private_key", encryptPassword, privateKeyString)
			if err != nil {
				return nil, err
			}
		}
		if seed != "" {
			seed, err = account.encryptForSerialization("seed", encryptPassword, account.Seed)
			if err != nil {
				return nil, err
			}
		}
	}
	generator, err := account.addressGeneratorObject()
	if err != nil {
		return nil, err
	}
	result := NewObject(
		Member{Key: "ledger", Value: account.Network.ID()},
		Member{Key: "name", Value: account.Name},
		Member{Key: "seed", Value: seed},
		Member{Key: "encrypted", Value: account.Encrypted || encryptPassword != ""},
		Member{Key: "private_key", Value: privateKeyString},
		Member{Key: "public_key", Value: account.PublicKey.ExtendedKeyString()},
		Member{Key: "address_generator", Value: generator},
		Member{Key: "modified_on", Value: new(big.Int).Set(account.ModifiedOn)},
	)
	if includeCertificates {
		result.Set("certificates", account.ChannelKeys)
	}
	return result, nil
}

func (account *Account) addressGeneratorObject() (*Object, error) {
	switch account.GeneratorName {
	case DeterministicChainGenerator:
		if account.Receiving == nil || account.Change == nil {
			return nil, fmt.Errorf("%w: deterministic managers are missing", ErrInvalidAccountData)
		}
		return NewObject(
			Member{Key: "name", Value: DeterministicChainGenerator},
			Member{Key: "receiving", Value: NewObject(
				Member{Key: "gap", Value: account.Receiving.Gap},
				Member{Key: "maximum_uses_per_address", Value: account.Receiving.MaximumUsesPerAddress},
			)},
			Member{Key: "change", Value: NewObject(
				Member{Key: "gap", Value: account.Change.Gap},
				Member{Key: "maximum_uses_per_address", Value: account.Change.MaximumUsesPerAddress},
			)},
		), nil
	case SingleAddressGenerator:
		return NewObject(Member{Key: "name", Value: SingleAddressGenerator}), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownAddressGenerator, account.GeneratorName)
	}
}

// Merge preserves the pinned mutation order, including partial mutation when
// a newer record fails after assigning name or modified_on.
func (account *Account) Merge(data *Object) error {
	if account == nil || account.ModifiedOn == nil || data == nil {
		return fmt.Errorf("%w: cannot merge nil account data", ErrInvalidAccountData)
	}
	incomingModified, hasModified := data.Get("modified_on")
	comparisonValue := incomingModified
	if !hasModified {
		comparisonValue = 0
	}
	newer, err := pythonNumberLess(account.ModifiedOn, comparisonValue)
	if err != nil {
		return fmt.Errorf("%w: compare modified_on: %v", ErrInvalidAccountData, err)
	}
	if newer {
		name, err := accountRequiredString(data, "name")
		if err != nil {
			return err
		}
		account.Name = name

		var modifiedOn *big.Int
		if hasModified {
			modifiedOn, err = accountPythonInt(incomingModified)
			if err != nil {
				return fmt.Errorf("%w: modified_on: %v", ErrInvalidAccountData, err)
			}
		} else {
			modifiedOn = accountPythonTimeInt(account.now())
		}
		account.ModifiedOn = modifiedOn

		generatorValue, exists := data.Get("address_generator")
		if !exists {
			return fmt.Errorf("%w: address_generator is missing", ErrInvalidAccountData)
		}
		generator, ok := generatorValue.(*Object)
		if !ok || generator == nil {
			return fmt.Errorf("%w: address_generator has type %T, want object", ErrInvalidAccountData, generatorValue)
		}
		generatorName, err := accountRequiredString(generator, "name")
		if err != nil {
			return err
		}
		if generatorName != account.GeneratorName {
			return fmt.Errorf("%w: local %q, incoming %q", ErrAddressGeneratorMismatch, account.GeneratorName, generatorName)
		}
		for _, chain := range []struct {
			name    string
			manager *AddressManager
		}{{"change", account.Change}, {"receiving", account.Receiving}} {
			value, exists := generator.Get(chain.name)
			if !exists || account.GeneratorName == SingleAddressGenerator {
				continue
			}
			incoming, ok := value.(*Object)
			if !ok || incoming == nil {
				return fmt.Errorf("%w: address_generator.%s has type %T, want object", ErrInvalidAccountData, chain.name, value)
			}
			if value, exists := incoming.Get("gap"); exists {
				chain.manager.Gap = value
			}
			if value, exists := incoming.Get("maximum_uses_per_address"); exists {
				chain.manager.MaximumUsesPerAddress = value
			}
		}
	}

	if certificatesValue, exists := data.Get("certificates"); exists {
		certificates, ok := certificatesValue.(*Object)
		if !ok || certificates == nil {
			return fmt.Errorf("%w: certificates has type %T, want object", ErrInvalidAccountData, certificatesValue)
		}
		for _, certificate := range certificates.Members() {
			account.ChannelKeys.Set(certificate.Key, certificate.Value)
		}
	}
	return nil
}

// Hash implements Account.hash: compact json.dumps output without
// certificates, followed by sorted certificate key names (values excluded).
func (account *Account) Hash() ([sha256.Size]byte, error) {
	if account == nil {
		return [sha256.Size]byte{}, fmt.Errorf("%w: account is nil", ErrInvalidAccountData)
	}
	if account.Encrypted {
		return [sha256.Size]byte{}, ErrEncryptedAccountHash
	}
	object, err := account.ToDict("", false)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	encoded, err := encodePreferenceJSON(object)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	hasher := sha256.New()
	_, _ = hasher.Write(encoded)
	certificateNames := account.ChannelKeys.Keys()
	sort.Strings(certificateNames)
	for _, name := range certificateNames {
		_, _ = hasher.Write([]byte(name))
	}
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return digest, nil
}

// Encrypt locks the account in place and reuses a cached IV for each secret.
func (account *Account) Encrypt(password string) error {
	if account == nil {
		return fmt.Errorf("%w: account is nil", ErrInvalidAccountData)
	}
	if account.Encrypted {
		return ErrAccountAlreadyEncrypted
	}
	if account.Seed != "" {
		encoded, err := account.encryptForSerialization("seed", password, account.Seed)
		if err != nil {
			return err
		}
		account.Seed = encoded
	}
	if account.PrivateKey != nil {
		encoded, err := account.encryptForSerialization(
			"private_key", password, account.PrivateKey.ExtendedKeyString(),
		)
		if err != nil {
			return err
		}
		account.PrivateKeyString = encoded
		account.PrivateKey = nil
	}
	account.Encrypted = true
	return nil
}

// Decrypt returns false for secret decryption, mnemonic validation, or private
// key parsing failures, matching the Python bool API. Successful decryption
// deliberately does not reconcile the decrypted private key with PublicKey.
func (account *Account) Decrypt(password string) (bool, error) {
	if account == nil {
		return false, fmt.Errorf("%w: account is nil", ErrInvalidAccountData)
	}
	if !account.Encrypted {
		return false, ErrAccountNotEncrypted
	}

	seed := ""
	if account.Seed != "" {
		plaintext, initializationVector, err := DecryptAccountSecret(password, account.Seed)
		if err != nil {
			return false, nil
		}
		account.initializationVectors["seed"] = append([]byte(nil), initializationVector...)
		if plaintext != "" {
			if _, err := mnemonic.NewEnglish().Decode(plaintext); err != nil {
				return false, nil
			}
		}
		seed = plaintext
	}

	var privateKey *keys.PrivateKey
	if account.PrivateKeyString != "" {
		plaintext, initializationVector, err := DecryptAccountSecret(password, account.PrivateKeyString)
		if err != nil {
			return false, nil
		}
		account.initializationVectors["private_key"] = append([]byte(nil), initializationVector...)
		if plaintext != "" {
			parsed, err := keys.ParseExtendedKey(account.Network, plaintext)
			if err != nil {
				return false, nil
			}
			var ok bool
			privateKey, ok = parsed.(*keys.PrivateKey)
			if !ok {
				return false, nil
			}
		}
	}

	account.Seed = seed
	account.PrivateKey = privateKey
	account.PrivateKeyString = ""
	account.Encrypted = false
	return true, nil
}

func (account *Account) encryptForSerialization(key, password, value string) (string, error) {
	initializationVector, err := account.getInitializationVector(key)
	if err != nil {
		return "", err
	}
	encoded, err := EncryptAccountSecret(password, value, initializationVector)
	if err != nil {
		return "", fmt.Errorf("encrypt account %s: %w", key, err)
	}
	return encoded, nil
}

func (account *Account) getInitializationVector(key string) ([]byte, error) {
	if account.initializationVectors == nil {
		account.initializationVectors = make(map[string][]byte)
	}
	if initializationVector, exists := account.initializationVectors[key]; exists {
		return initializationVector, nil
	}
	initializationVector := make([]byte, 16)
	entropy := account.entropy
	if entropy == nil {
		entropy = rand.Reader
	}
	if _, err := io.ReadFull(entropy, initializationVector); err != nil {
		return nil, fmt.Errorf("generate account %s initialization vector: %w", key, err)
	}
	account.initializationVectors[key] = initializationVector
	return initializationVector, nil
}

func (account *Account) SetInitializationVector(key string, initializationVector []byte) error {
	if account == nil {
		return fmt.Errorf("%w: account is nil", ErrInvalidAccountData)
	}
	if len(initializationVector) != 16 {
		return fmt.Errorf("%w: got %d bytes, want 16", ErrInvalidAccountIV, len(initializationVector))
	}
	if account.initializationVectors == nil {
		account.initializationVectors = make(map[string][]byte)
	}
	account.initializationVectors[key] = append([]byte(nil), initializationVector...)
	return nil
}

func (account *Account) InitializationVector(key string) ([]byte, bool) {
	if account == nil {
		return nil, false
	}
	initializationVector, exists := account.initializationVectors[key]
	return append([]byte(nil), initializationVector...), exists
}

func accountString(data *Object, key, fallback string) (string, error) {
	value, exists := data.Get(key)
	if !exists || value == nil {
		return fallback, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%w: %s has type %T, want string", ErrInvalidAccountData, key, value)
	}
	return text, nil
}

func accountRequiredString(data *Object, key string) (string, error) {
	value, exists := data.Get(key)
	if !exists {
		return "", fmt.Errorf("%w: %s is missing", ErrInvalidAccountData, key)
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%w: %s has type %T, want string", ErrInvalidAccountData, key, value)
	}
	return text, nil
}

func accountBool(data *Object, key string, fallback bool) (bool, error) {
	value, exists := data.Get(key)
	if !exists || value == nil {
		return fallback, nil
	}
	boolean, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%w: %s has type %T, want bool", ErrInvalidAccountData, key, value)
	}
	return boolean, nil
}

func accountPythonInt(value any) (*big.Int, error) {
	if value == nil {
		return nil, errors.New("int() argument must not be None")
	}
	switch typed := value.(type) {
	case bool:
		if typed {
			return big.NewInt(1), nil
		}
		return new(big.Int), nil
	case json.Number:
		return accountPythonIntNumber(string(typed))
	case *big.Int:
		if typed == nil {
			return nil, errors.New("int() argument is nil")
		}
		return new(big.Int).Set(typed), nil
	case big.Int:
		return new(big.Int).Set(&typed), nil
	case float64:
		return accountPythonIntFloat(typed)
	case float32:
		return accountPythonIntFloat(float64(typed))
	case string:
		return accountPythonIntString(typed)
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return big.NewInt(reflected.Int()), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return new(big.Int).SetUint64(reflected.Uint()), nil
	}
	return nil, fmt.Errorf("int() argument has type %T", value)
}

func accountPythonIntNumber(value string) (*big.Int, error) {
	if !strings.ContainsAny(value, ".eE") {
		if integer, ok := new(big.Int).SetString(value, 10); ok {
			return integer, nil
		}
	}
	floatValue, err := strconv.ParseFloat(value, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		// ParseFloat returns a usable infinity with ErrRange; Python int then
		// raises for that infinity below.
		return nil, err
	}
	return accountPythonIntFloat(floatValue)
}

func accountPythonTimeInt(value time.Time) *big.Int {
	seconds := value.Unix()
	// time.Unix floors negative fractional timestamps while int(time.time())
	// truncates toward zero.
	if seconds < 0 && value.Nanosecond() != 0 {
		seconds++
	}
	return big.NewInt(seconds)
}

func accountPythonIntFloat(value float64) (*big.Int, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, errors.New("cannot convert NaN or infinity to integer")
	}
	integer, _ := new(big.Float).SetFloat64(value).Int(nil)
	return integer, nil
}

func accountPythonIntString(value string) (*big.Int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("invalid literal for int()")
	}
	// Python permits underscores only between decimal digits.
	for index, character := range value {
		if character != '_' {
			continue
		}
		if index == 0 || index == len(value)-1 || value[index-1] < '0' || value[index-1] > '9' || value[index+1] < '0' || value[index+1] > '9' {
			return nil, errors.New("invalid literal for int()")
		}
	}
	value = strings.ReplaceAll(value, "_", "")
	integer, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return nil, errors.New("invalid literal for int()")
	}
	return integer, nil
}
