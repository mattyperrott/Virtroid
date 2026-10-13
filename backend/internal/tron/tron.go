// Package tron derives the TRC20 deposit addresses accounts pay into.
//
// Addresses come from a BIP39 seed at m/44'/195'/0'/0/i, the path Tron wallets
// such as TronLink use, so the seed also opens them there. The control plane
// only holds the extended public key of m/44'/195'/0'/0 and derives addresses
// from it; the payments service holds the seed.
package tron

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/cosmos/go-bip39"
	"golang.org/x/crypto/sha3"
)

const (
	addressVersion = 0x41
	coinType       = 195
	// depositPathDepth is the depth of m/44'/195'/0'/0.
	depositPathDepth = 4
)

// USDTContract is Tether's TRC20 contract on the Tron mainnet.
const USDTContract = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"

// AddressFromPublicKey is the base58 Tron address of a secp256k1 key.
func AddressFromPublicKey(key *btcec.PublicKey) string {
	return encodeAddress(addressHash(key))
}

func addressHash(key *btcec.PublicKey) []byte {
	hash := sha3.NewLegacyKeccak256()
	_, _ = hash.Write(key.SerializeUncompressed()[1:])
	return hash.Sum(nil)[12:]
}

func encodeAddress(hash20 []byte) string {
	return base58.CheckEncode(hash20, addressVersion)
}

// AddressBytes is the 21-byte form of an address (0x41 and the hash), as
// contracts and transactions carry it.
func AddressBytes(address string) ([]byte, error) {
	decoded, version, err := base58.CheckDecode(strings.TrimSpace(address))
	if err != nil {
		return nil, fmt.Errorf("invalid tron address: %w", err)
	}
	if version != addressVersion || len(decoded) != 20 {
		return nil, errors.New("invalid tron address")
	}
	return append([]byte{addressVersion}, decoded...), nil
}

// AddressFromBytes encodes a 21-byte address.
func AddressFromBytes(raw []byte) (string, error) {
	if len(raw) != 21 || raw[0] != addressVersion {
		return "", errors.New("invalid tron address bytes")
	}
	return encodeAddress(raw[1:]), nil
}

// ValidAddress reports whether address is a well-formed Tron address.
func ValidAddress(address string) bool {
	_, err := AddressBytes(address)
	return err == nil
}

// DepositKey derives deposit addresses from the extended public key of
// m/44'/195'/0'/0.
type DepositKey struct {
	key *hdkeychain.ExtendedKey
}

// ParseDepositXPub reads the extended public key the control plane holds.
func ParseDepositXPub(xpub string) (*DepositKey, error) {
	key, err := hdkeychain.NewKeyFromString(strings.TrimSpace(xpub))
	if err != nil {
		return nil, fmt.Errorf("invalid deposit xpub: %w", err)
	}
	if key.IsPrivate() {
		return nil, errors.New("the deposit key must be public; never give the control plane the private key")
	}
	if key.Depth() != depositPathDepth {
		return nil, fmt.Errorf("the deposit xpub must be at m/44'/195'/0'/0 (depth %d), not depth %d", depositPathDepth, key.Depth())
	}
	return &DepositKey{key: key}, nil
}

// Address is the deposit address with the given index.
func (d *DepositKey) Address(index uint32) (string, error) {
	if index >= hdkeychain.HardenedKeyStart {
		return "", errors.New("deposit index is out of range")
	}
	child, err := d.key.Derive(index)
	if err != nil {
		return "", err
	}
	public, err := child.ECPubKey()
	if err != nil {
		return "", err
	}
	return AddressFromPublicKey(public), nil
}

// Wallet is the hot side: the seed's keys under m/44'/195'/0'/0.
type Wallet struct {
	deposits *hdkeychain.ExtendedKey
}

// NewSeedPhrase is a fresh 24-word BIP39 phrase.
func NewSeedPhrase() (string, error) {
	entropy, err := bip39.NewEntropy(256)
	if err != nil {
		return "", err
	}
	return bip39.NewMnemonic(entropy)
}

// OpenWallet derives the deposit keys from a BIP39 phrase.
func OpenWallet(phrase string) (*Wallet, error) {
	phrase = strings.Join(strings.Fields(phrase), " ")
	if !bip39.IsMnemonicValid(phrase) {
		return nil, errors.New("invalid seed phrase")
	}
	seed := bip39.NewSeed(phrase, "")
	defer clear(seed)
	key, err := receivingKey(seed, coinType)
	if err != nil {
		return nil, err
	}
	return &Wallet{deposits: key}, nil
}

// receivingKey is m/44'/coin'/0'/0 of a BIP39 seed.
func receivingKey(seed []byte, coin uint32) (*hdkeychain.ExtendedKey, error) {
	key, err := hdkeychain.NewMaster(seed, &chaincfg.MainNetParams)
	if err != nil {
		return nil, err
	}
	for _, step := range []uint32{
		hdkeychain.HardenedKeyStart + 44,
		hdkeychain.HardenedKeyStart + coin,
		hdkeychain.HardenedKeyStart,
		0,
	} {
		if key, err = key.Derive(step); err != nil {
			return nil, err
		}
	}
	return key, nil
}

// XPub is the extended public key the control plane derives addresses from.
func (w *Wallet) XPub() (string, error) {
	public, err := w.deposits.Neuter()
	if err != nil {
		return "", err
	}
	return public.String(), nil
}

// Key is the private key of the deposit address with the given index.
func (w *Wallet) Key(index uint32) (*btcec.PrivateKey, error) {
	if index >= hdkeychain.HardenedKeyStart {
		return nil, errors.New("deposit index is out of range")
	}
	child, err := w.deposits.Derive(index)
	if err != nil {
		return nil, err
	}
	return child.ECPrivKey()
}

// Address is the deposit address with the given index.
func (w *Wallet) Address(index uint32) (string, error) {
	key, err := w.Key(index)
	if err != nil {
		return "", err
	}
	return AddressFromPublicKey(key.PubKey()), nil
}

// TxID is the transaction ID Tron signs: the SHA-256 of the raw transaction.
func TxID(rawData []byte) [32]byte {
	return sha256.Sum256(rawData)
}
