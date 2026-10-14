package tron

import (
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustAddress(t *testing.T, hexAddress string) string {
	t.Helper()
	address, err := AddressFromBytes(mustHex(t, hexAddress))
	if err != nil {
		t.Fatal(err)
	}
	return address
}

// Mainnet transactions from block 87105266 must encode to the same bytes,
// ID and signer as on the chain.
func TestTransactionsMatchMainnet(t *testing.T) {
	anyRef := BlockRef{Number: 1, ID: make([]byte, 32)}
	cases := []struct {
		name      string
		build     func() (RawTransaction, error)
		refBytes  string
		refHash   string
		expires   int64
		timestamp int64
		rawHex    string
		txID      string
		signature string
		owner     string
	}{
		{
			name: "TRX",
			build: func() (RawTransaction, error) {
				return SendTRX(mustAddress(t, "41832cc5f732d79329d08ab5134ec0dafe22b33809"),
					mustAddress(t, "4157661a550ac4935b5317d287da1c54edd5b0fab1"), 1, anyRef, time.Now())
			},
			refBytes: "1ede", refHash: "bcc9728d15b29e4a", expires: 1791988764000, timestamp: 1791988707191,
			rawHex:    "0a021ede2208bcc9728d15b29e4a40e09aead693345a65080112610a2d747970652e676f6f676c65617069732e636f6d2f70726f746f636f6c2e5472616e73666572436f6e747261637412300a1541832cc5f732d79329d08ab5134ec0dafe22b3380912154157661a550ac4935b5317d287da1c54edd5b0fab1180170f7dee6d69334",
			txID:      "fadf38e11b1232ee1a002e1a8cd142753303883e765782d3739d9c3a305c9049",
			signature: "084163429d40b283ca88e4e32c75166a776819ae31645dd3cfa5d324d7c60e697644fb3ff8183a707078ae28735f8e0d1838de8bd12665e5df1b0a95b085bdfe00",
			owner:     "41832cc5f732d79329d08ab5134ec0dafe22b33809",
		},
		{
			name: "USDT",
			build: func() (RawTransaction, error) {
				return SendTRC20(mustAddress(t, "414f4a8b86d683ad09db07f1d69b610b83a4c32c2e"),
					mustAddress(t, "41a614f803b6fd780986a42c78ec9c7f77e6ded13c"),
					mustAddress(t, "41461651e5cbcf89e1435200cd5d1d323ab9c62a44"),
					big.NewInt(3_000_000_000), 50_000_000, anyRef, time.Now())
			},
			refBytes: "1eda", refHash: "219558e117d16dec", expires: 1791988994549, timestamp: 1791988694549,
			rawHex:    "0a021eda2208219558e117d16dec40f5a3f8d693345aae01081f12a9010a31747970652e676f6f676c65617069732e636f6d2f70726f746f636f6c2e54726967676572536d617274436f6e747261637412740a15414f4a8b86d683ad09db07f1d69b610b83a4c32c2e121541a614f803b6fd780986a42c78ec9c7f77e6ded13c2244a9059cbb000000000000000000000000461651e5cbcf89e1435200cd5d1d323ab9c62a4400000000000000000000000000000000000000000000000000000000b2d05e007095fce5d69334900180e1eb17",
			txID:      "0f51e1dac56683cfd416f3a15a0598a1c2390fd2889d946952dd825be029f52b",
			signature: "f72603c3236ccc0239c3e7cbcc0729c01ce50bfd8857d58cc72d81c37adc9dbc5c851e2c1c34387af59001a28eed076eabf1a4a85d6c08a89f22c5523b7b7ef300",
			owner:     "414f4a8b86d683ad09db07f1d69b610b83a4c32c2e",
		},
	}
	for _, tc := range cases {
		raw, err := tc.build()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		raw.RefBlockBytes, raw.RefBlockHash = mustHex(t, tc.refBytes), mustHex(t, tc.refHash)
		raw.Expiration, raw.Timestamp = tc.expires, tc.timestamp
		encoded := raw.Encode()
		if got := hex.EncodeToString(encoded); got != tc.rawHex {
			t.Fatalf("%s raw =\n%s\nwant\n%s", tc.name, got, tc.rawHex)
		}
		if id := TxID(encoded); hex.EncodeToString(id[:]) != tc.txID {
			t.Fatalf("%s txID = %x", tc.name, id)
		}
		signer, err := Signer(encoded, mustHex(t, tc.signature))
		if err != nil || signer != mustAddress(t, tc.owner) {
			t.Fatalf("%s signer = %s, %v", tc.name, signer, err)
		}
	}
}

func TestBlockReferenceFromMainnetBlock(t *testing.T) {
	raw, err := SendTRX(mustAddress(t, "41832cc5f732d79329d08ab5134ec0dafe22b33809"),
		mustAddress(t, "4157661a550ac4935b5317d287da1c54edd5b0fab1"), 1,
		BlockRef{Number: 87105266, ID: mustHex(t, "0000000005311ef2ba4e18d3457f9ded53695954a7056d4cf55f6d42abb609af")},
		time.UnixMilli(1791988710000))
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(raw.RefBlockBytes) != "1ef2" || hex.EncodeToString(raw.RefBlockHash) != "ba4e18d3457f9ded" {
		t.Fatalf("ref = %x %x", raw.RefBlockBytes, raw.RefBlockHash)
	}
	if raw.Expiration-raw.Timestamp != 60_000 {
		t.Fatalf("lifetime = %d ms", raw.Expiration-raw.Timestamp)
	}
}

func TestSignRecoversTheSigner(t *testing.T) {
	key, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	from := AddressFromPublicKey(key.PubKey())
	raw, err := SendTRX(from, mustAddress(t, "4157661a550ac4935b5317d287da1c54edd5b0fab1"), 5_000_000,
		BlockRef{Number: 87105266, ID: make([]byte, 32)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	encoded := raw.Encode()
	signature := Sign(encoded, key)
	if len(signature) != 65 || signature[64] > 1 {
		t.Fatalf("signature = %x", signature)
	}
	if signer, err := Signer(encoded, signature); err != nil || signer != from {
		t.Fatalf("signer = %s, %v; want %s", signer, err, from)
	}
	if got := SignedTransaction(encoded, signature); len(got) != len(encoded)+len(signature)+5 {
		t.Fatalf("signed transaction is %d bytes", len(got))
	}
}
