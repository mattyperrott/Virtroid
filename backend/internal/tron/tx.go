package tron

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

// Transactions are built and signed here rather than by a node, so a node
// can only fail to relay one, never change what is signed. Only the two
// kinds the payments service sends are encoded: sending TRX, and calling a
// TRC20 token's transfer.

// BlockRef is the recent block a transaction names, which keeps it from
// being replayed on a fork that lacks that block.
type BlockRef struct {
	Number int64
	ID     []byte // 32-byte block ID
}

// RawTransaction is a transaction's signed part (protocol.Transaction.raw).
type RawTransaction struct {
	RefBlockBytes []byte
	RefBlockHash  []byte
	Expiration    int64 // unix milliseconds
	Timestamp     int64 // unix milliseconds
	FeeLimit      int64 // sun, for contract calls

	contractType int
	typeURL      string
	value        []byte
}

const (
	transferContractType       = 1
	triggerSmartContractType   = 31
	transferContractURL        = "type.googleapis.com/protocol.TransferContract"
	triggerSmartContractURL    = "type.googleapis.com/protocol.TriggerSmartContract"
	trc20TransferSelector      = "a9059cbb"
	defaultTransactionLifetime = 60 * time.Second
)

func newRaw(ref BlockRef, now time.Time) (RawTransaction, error) {
	if len(ref.ID) != 32 || ref.Number <= 0 {
		return RawTransaction{}, errors.New("a block reference needs the block's number and 32-byte ID")
	}
	number := make([]byte, 8)
	binary.BigEndian.PutUint64(number, uint64(ref.Number))
	return RawTransaction{
		RefBlockBytes: number[6:8],
		RefBlockHash:  append([]byte(nil), ref.ID[8:16]...),
		Timestamp:     now.UnixMilli(),
		Expiration:    now.Add(defaultTransactionLifetime).UnixMilli(),
	}, nil
}

// SendTRX is a transaction sending amountSun (millionths of a TRX).
func SendTRX(from, to string, amountSun int64, ref BlockRef, now time.Time) (RawTransaction, error) {
	if amountSun <= 0 {
		return RawTransaction{}, errors.New("amount must be positive")
	}
	owner, err := AddressBytes(from)
	if err != nil {
		return RawTransaction{}, fmt.Errorf("from: %w", err)
	}
	receiver, err := AddressBytes(to)
	if err != nil {
		return RawTransaction{}, fmt.Errorf("to: %w", err)
	}
	raw, err := newRaw(ref, now)
	if err != nil {
		return RawTransaction{}, err
	}
	var value []byte
	value = appendBytesField(value, 1, owner)
	value = appendBytesField(value, 2, receiver)
	value = appendVarintField(value, 3, uint64(amountSun))
	raw.contractType, raw.typeURL, raw.value = transferContractType, transferContractURL, value
	return raw, nil
}

// SendTRC20 is a call of token's transfer(to, amount), amount in the
// token's base units, burning at most feeLimitSun for energy.
func SendTRC20(from, token, to string, amount *big.Int, feeLimitSun int64, ref BlockRef, now time.Time) (RawTransaction, error) {
	if amount == nil || amount.Sign() <= 0 || amount.BitLen() > 256 {
		return RawTransaction{}, errors.New("amount must be positive")
	}
	if feeLimitSun <= 0 {
		return RawTransaction{}, errors.New("a contract call needs a fee limit")
	}
	owner, err := AddressBytes(from)
	if err != nil {
		return RawTransaction{}, fmt.Errorf("from: %w", err)
	}
	contract, err := AddressBytes(token)
	if err != nil {
		return RawTransaction{}, fmt.Errorf("token: %w", err)
	}
	receiver, err := AddressBytes(to)
	if err != nil {
		return RawTransaction{}, fmt.Errorf("to: %w", err)
	}
	raw, err := newRaw(ref, now)
	if err != nil {
		return RawTransaction{}, err
	}
	data, _ := hex.DecodeString(trc20TransferSelector)
	data = append(data, make([]byte, 12)...)
	data = append(data, receiver[1:]...) // the 20-byte account, without Tron's 0x41
	data = append(data, amount.FillBytes(make([]byte, 32))...)
	var value []byte
	value = appendBytesField(value, 1, owner)
	value = appendBytesField(value, 2, contract)
	value = appendBytesField(value, 4, data)
	raw.FeeLimit = feeLimitSun
	raw.contractType, raw.typeURL, raw.value = triggerSmartContractType, triggerSmartContractURL, value
	return raw, nil
}

// Encode is the transaction's raw bytes, which its ID and signature cover.
func (r RawTransaction) Encode() []byte {
	var parameter []byte
	parameter = appendBytesField(parameter, 1, []byte(r.typeURL))
	parameter = appendBytesField(parameter, 2, r.value)
	var contract []byte
	contract = appendVarintField(contract, 1, uint64(r.contractType))
	contract = appendBytesField(contract, 2, parameter)

	var out []byte
	out = appendBytesField(out, 1, r.RefBlockBytes)
	out = appendBytesField(out, 4, r.RefBlockHash)
	out = appendVarintField(out, 8, uint64(r.Expiration))
	out = appendBytesField(out, 11, contract)
	out = appendVarintField(out, 14, uint64(r.Timestamp))
	if r.FeeLimit > 0 {
		out = appendVarintField(out, 18, uint64(r.FeeLimit))
	}
	return out
}

// Sign signs raw transaction bytes: r || s || v, v the recovery ID (0 or 1).
func Sign(rawData []byte, key *btcec.PrivateKey) []byte {
	id := TxID(rawData)
	compact := ecdsa.SignCompact(key, id[:], false) // [27 + v] || r || s
	return append(append([]byte(nil), compact[1:]...), compact[0]-27)
}

// Signer is the address whose key made signature over rawData.
func Signer(rawData, signature []byte) (string, error) {
	if len(signature) != 65 || signature[64] > 3 {
		return "", errors.New("signature must be 65 bytes ending in a recovery ID")
	}
	id := TxID(rawData)
	compact := append([]byte{27 + signature[64]}, signature[:64]...)
	key, _, err := ecdsa.RecoverCompact(compact, id[:])
	if err != nil {
		return "", err
	}
	return AddressFromPublicKey(key), nil
}

// SignedTransaction is a signed transaction's bytes (protocol.Transaction),
// what /wallet/broadcasthex takes.
func SignedTransaction(rawData, signature []byte) []byte {
	var out []byte
	out = appendBytesField(out, 1, rawData)
	return appendBytesField(out, 2, signature)
}

func appendVarintField(out []byte, field int, value uint64) []byte {
	out = binary.AppendUvarint(out, uint64(field)<<3)
	return binary.AppendUvarint(out, value)
}

func appendBytesField(out []byte, field int, value []byte) []byte {
	out = binary.AppendUvarint(out, uint64(field)<<3|2)
	out = binary.AppendUvarint(out, uint64(len(value)))
	return append(out, value...)
}
