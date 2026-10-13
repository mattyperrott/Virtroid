package tron

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/cosmos/go-bip39"
)

func TestAddressEncodingMatchesTheUSDTContract(t *testing.T) {
	raw, err := AddressBytes(USDTContract)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(raw); got != "41a614f803b6fd780986a42c78ec9c7f77e6ded13c" {
		t.Fatalf("USDT contract bytes = %s", got)
	}
	back, err := AddressFromBytes(raw)
	if err != nil || back != USDTContract {
		t.Fatalf("round trip = %s err=%v", back, err)
	}
	for _, bad := range []string{"", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6u", "0xa614f803b6fd780986a42c78ec9c7f77e6ded13c", "1BoatSLRHtKNngkdXEeobR76b53LETtpyT"} {
		if ValidAddress(bad) {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestAddressHashIsTheEthereumAddressOfTheKey(t *testing.T) {
	// Private key 1's Ethereum address is 0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf.
	key, _ := btcec.PrivKeyFromBytes(append(make([]byte, 31), 1))
	if got := hex.EncodeToString(addressHash(key.PubKey())); got != "7e5f4552091a69125d5dfcb7b8c2659029395bdf" {
		t.Fatalf("address hash = %s", got)
	}
	if !strings.HasPrefix(AddressFromPublicKey(key.PubKey()), "T") {
		t.Fatal("tron addresses start with T")
	}
}

func TestSeedDerivationMatchesTheStandardTestVector(t *testing.T) {
	// "abandon ... about" at m/44'/60'/0'/0/0 is 0x9858EfFD232B4033E47d90003D41EC34EcaEda94;
	// Tron uses the same derivation under coin type 195.
	seed := bip39.NewSeed("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about", "")
	key, err := receivingKey(seed, 60)
	if err != nil {
		t.Fatal(err)
	}
	child, err := key.Derive(0)
	if err != nil {
		t.Fatal(err)
	}
	public, err := child.ECPubKey()
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(addressHash(public)); got != "9858effd232b4033e47d90003d41ec34ecaeda94" {
		t.Fatalf("first address hash = %s", got)
	}
}

func TestXPubDerivesTheWalletsAddresses(t *testing.T) {
	phrase, err := NewSeedPhrase()
	if err != nil {
		t.Fatal(err)
	}
	if words := len(strings.Fields(phrase)); words != 24 {
		t.Fatalf("seed phrase has %d words", words)
	}
	wallet, err := OpenWallet(phrase)
	if err != nil {
		t.Fatal(err)
	}
	xpub, err := wallet.XPub()
	if err != nil {
		t.Fatal(err)
	}
	deposits, err := ParseDepositXPub(xpub)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, index := range []uint32{0, 1, 7, 1 << 20} {
		fromXPub, err := deposits.Address(index)
		if err != nil {
			t.Fatal(err)
		}
		fromSeed, err := wallet.Address(index)
		if err != nil {
			t.Fatal(err)
		}
		if fromXPub != fromSeed || !ValidAddress(fromXPub) || seen[fromXPub] {
			t.Fatalf("index %d: xpub %s, seed %s", index, fromXPub, fromSeed)
		}
		seen[fromXPub] = true
	}
	if _, err := OpenWallet("abandon abandon abandon"); err == nil {
		t.Fatal("an invalid phrase opened a wallet")
	}
}

func TestParseDepositXPubRefusesPrivateAndWrongDepthKeys(t *testing.T) {
	wallet, err := OpenWallet("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseDepositXPub(wallet.deposits.String()); err == nil {
		t.Fatal("a private key was accepted as the deposit xpub")
	}
	child, _ := wallet.deposits.Derive(0)
	public, _ := child.Neuter()
	if _, err := ParseDepositXPub(public.String()); err == nil {
		t.Fatal("an xpub at the wrong depth was accepted")
	}
}
