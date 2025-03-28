package common

import (
	"crypto/sha512"
	"encoding/binary"
	"fmt"

	"filippo.io/edwards25519"
	"github.com/MixinNetwork/mixin/crypto"
)

const (
	TxVersionHashSignature = 0x05

	ExtraSizeGeneralLimit    = 256
	ExtraSizeStorageStep     = 1024
	ExtraSizeStorageCapacity = 1024 * 1024 * 4
	ExtraStoragePriceStep    = "0.0001"
	SliceCountLimit          = 256
	ReferencesCountLimit     = 16

	OutputTypeScript               = 0x00
	OutputTypeWithdrawalSubmit     = 0xa1
	OutputTypeNodePledge           = 0xa3
	OutputTypeNodeAccept           = 0xa4
	outputTypeNodeResign           = 0xa5
	OutputTypeNodeRemove           = 0xa6
	OutputTypeWithdrawalClaim      = 0xa9
	OutputTypeNodeCancel           = 0xaa
	OutputTypeCustodianUpdateNodes = 0xb1
	OutputTypeCustodianSlashNodes  = 0xb2

	TransactionTypeScript               = 0x00
	TransactionTypeMint                 = 0x01
	TransactionTypeDeposit              = 0x02
	TransactionTypeWithdrawalSubmit     = 0x03
	TransactionTypeWithdrawalClaim      = 0x05
	TransactionTypeNodePledge           = 0x06
	TransactionTypeNodeAccept           = 0x07
	transactionTypeNodeResign           = 0x08
	TransactionTypeNodeRemove           = 0x09
	TransactionTypeNodeCancel           = 0x12
	TransactionTypeCustodianUpdateNodes = 0x13
	TransactionTypeCustodianSlashNodes  = 0x14
	TransactionTypeUnknown              = 0xff
)

type Input struct {
	Hash    crypto.Hash
	Index   uint
	Genesis []byte
	Deposit *DepositData
	Mint    *MintData
}

type Output struct {
	Type   uint8
	Amount Integer

	Keys   []*crypto.Key
	Mask   crypto.Key
	Script Script

	Withdrawal *WithdrawalData
}

type Transaction struct {
	Version    uint8
	Asset      crypto.Hash
	Inputs     []*Input
	Outputs    []*Output
	References []crypto.Hash
	Extra      []byte
}

type SignedTransaction struct {
	Transaction
	AggregatedSignature *AggregatedSignature
	SignaturesMap       []map[uint16]*crypto.Signature
}

func (tx *Transaction) ViewGhostKey(a *crypto.Key) []*Output {
	outputs := make([]*Output, 0)

	for i, o := range tx.Outputs {
		if o.Type != OutputTypeScript {
			continue
		}

		out := &Output{
			Type:   o.Type,
			Amount: o.Amount,
			Script: o.Script,
			Mask:   o.Mask,
		}
		for _, k := range o.Keys {
			key := crypto.ViewGhostOutputKey(k, a, &o.Mask, uint64(i))
			out.Keys = append(out.Keys, key)
		}
		outputs = append(outputs, out)
	}

	return outputs
}

func (tx *SignedTransaction) TransactionType() uint8 {
	for _, in := range tx.Inputs {
		if in.Mint != nil {
			return TransactionTypeMint
		}
		if in.Deposit != nil {
			return TransactionTypeDeposit
		}
		if in.Genesis != nil {
			return TransactionTypeUnknown
		}
	}

	isScript := true
	for _, out := range tx.Outputs {
		switch out.Type {
		case OutputTypeWithdrawalSubmit:
			return TransactionTypeWithdrawalSubmit
		case OutputTypeWithdrawalClaim:
			return TransactionTypeWithdrawalClaim
		case OutputTypeNodePledge:
			return TransactionTypeNodePledge
		case OutputTypeNodeCancel:
			return TransactionTypeNodeCancel
		case OutputTypeNodeAccept:
			return TransactionTypeNodeAccept
		case OutputTypeNodeRemove:
			return TransactionTypeNodeRemove
		case OutputTypeCustodianUpdateNodes:
			return TransactionTypeCustodianUpdateNodes
		case OutputTypeCustodianSlashNodes:
			return TransactionTypeCustodianSlashNodes
		}
		isScript = isScript && out.Type == OutputTypeScript
	}

	if isScript {
		return TransactionTypeScript
	}
	return TransactionTypeUnknown
}

func (signed *SignedTransaction) SignUTXO(utxo *UTXO, accounts []*Address) error {
	msg := signed.AsVersioned().PayloadHash()

	if len(accounts) == 0 {
		return nil
	}

	keysFilter := make(map[string]uint16)
	for i, k := range utxo.Keys {
		keysFilter[k.String()] = uint16(i)
	}

	sigs := make(map[uint16]*crypto.Signature)
	for _, acc := range accounts {
		priv := crypto.DeriveGhostPrivateKey(&utxo.Mask, &acc.PrivateViewKey, &acc.PrivateSpendKey, uint64(utxo.Index))
		i, found := keysFilter[priv.Public().String()]
		if !found {
			return fmt.Errorf("invalid key for the input %s", acc.String())
		}
		sig := priv.Sign(msg)
		sigs[i] = &sig
	}
	signed.SignaturesMap = append(signed.SignaturesMap, sigs)
	return nil
}

func (signed *SignedTransaction) SignInput(reader UTXOKeysReader, index int, accounts []*Address) error {
	if len(accounts) == 0 {
		return nil
	}
	if index >= len(signed.Inputs) {
		return fmt.Errorf("invalid input index %d/%d", index, len(signed.Inputs))
	}
	in := signed.Inputs[index]
	if in.Deposit != nil || in.Mint != nil {
		return signed.SignRaw(accounts[0].PrivateSpendKey)
	}

	utxo, err := reader.ReadUTXOKeys(in.Hash, in.Index)
	if err != nil {
		return err
	}
	if utxo == nil {
		return fmt.Errorf("input not found %s:%d", in.Hash.String(), in.Index)
	}

	keysFilter := make(map[string]uint16)
	for i, k := range utxo.Keys {
		keysFilter[k.String()] = uint16(i)
	}

	sigs := make(map[uint16]*crypto.Signature)
	msg := signed.AsVersioned().PayloadHash()
	for _, acc := range accounts {
		priv := crypto.DeriveGhostPrivateKey(&utxo.Mask, &acc.PrivateViewKey, &acc.PrivateSpendKey, uint64(in.Index))
		i, found := keysFilter[priv.Public().String()]
		if !found {
			return fmt.Errorf("invalid key for the input %s", acc.String())
		}
		sig := priv.Sign(msg)
		sigs[i] = &sig
	}
	signed.SignaturesMap = append(signed.SignaturesMap, sigs)
	return nil
}

func (signed *SignedTransaction) SignRaw(key crypto.Key) error {
	msg := signed.AsVersioned().PayloadHash()

	if len(signed.Inputs) != 1 {
		return fmt.Errorf("invalid inputs count %d", len(signed.Inputs))
	}
	in := signed.Inputs[0]
	if in.Deposit == nil && in.Mint == nil {
		return fmt.Errorf("invalid input format")
	}
	sig := key.Sign(msg)
	sigs := map[uint16]*crypto.Signature{0: &sig}
	signed.SignaturesMap = append(signed.SignaturesMap, sigs)
	return nil
}

func (signed *SignedTransaction) AggregateSign(reader UTXOKeysReader, accounts [][]*Address, seed []byte) error {
	var signers []int
	var randoms []*crypto.Key
	var pubKeys, privKeys []*crypto.Key
	for index, in := range signed.Inputs {
		utxo, err := reader.ReadUTXOKeys(in.Hash, in.Index)
		if err != nil {
			return err
		}
		if utxo == nil {
			return fmt.Errorf("input not found %s:%d", in.Hash.String(), in.Index)
		}

		keysFilter := make(map[string]int)
		for i, k := range utxo.Keys {
			keysFilter[k.String()] = i
		}

		for _, acc := range accounts[index] {
			priv := crypto.DeriveGhostPrivateKey(&utxo.Mask, &acc.PrivateViewKey, &acc.PrivateSpendKey, uint64(in.Index))
			i, found := keysFilter[priv.Public().String()]
			if !found {
				return fmt.Errorf("invalid key for the input %s", acc.String())
			}
			m := len(pubKeys) + i
			if sl := len(signers); sl > 0 && m <= signers[sl-1] {
				return fmt.Errorf("invalid signers order %d %d", signers[sl-1], m)
			}
			signers = append(signers, m)
			privKeys = append(privKeys, priv)
		}
		pubKeys = append(pubKeys, utxo.Keys...)
	}

	P := edwards25519.NewIdentityPoint()
	A := edwards25519.NewIdentityPoint()
	for _, m := range signers {
		buf := binary.BigEndian.AppendUint16(seed, uint16(m))
		s := crypto.Blake3Hash(buf)
		r := crypto.NewKeyFromSeed(append(s[:], s[:]...))
		randoms = append(randoms, &r)
		R := r.Public()

		p, err := edwards25519.NewIdentityPoint().SetBytes(R[:])
		if err != nil {
			return err
		}
		P = P.Add(P, p)

		pub := pubKeys[m]
		a, err := edwards25519.NewIdentityPoint().SetBytes(pub[:])
		if err != nil {
			return err
		}
		A = A.Add(A, a)
	}

	var hramDigest [64]byte
	msg := signed.AsVersioned().PayloadHash()
	h := sha512.New()
	h.Write(P.Bytes())
	h.Write(A.Bytes())
	h.Write(msg[:])
	h.Sum(hramDigest[:0])
	x, err := edwards25519.NewScalar().SetUniformBytes(hramDigest[:])
	if err != nil {
		return err
	}

	S := edwards25519.NewScalar()
	for i, k := range privKeys {
		y, err := edwards25519.NewScalar().SetCanonicalBytes(k[:])
		if err != nil {
			panic(k.String())
		}
		z, err := edwards25519.NewScalar().SetCanonicalBytes(randoms[i][:])
		if err != nil {
			panic(randoms[i].String())
		}
		s := edwards25519.NewScalar().MultiplyAdd(x, y, z)
		S = S.Add(S, s)
	}

	as := &AggregatedSignature{Signers: signers}
	copy(as.Signature[:32], P.Bytes())
	copy(as.Signature[32:], S.Bytes())
	signed.AggregatedSignature = as
	return nil
}

func NewTransactionV5(asset crypto.Hash) *Transaction {
	return &Transaction{
		Version: TxVersionHashSignature,
		Asset:   asset,
	}
}

func (tx *Transaction) AddInput(hash crypto.Hash, index uint) {
	in := &Input{
		Hash:  hash,
		Index: index,
	}
	tx.Inputs = append(tx.Inputs, in)
}

func (tx *Transaction) AddOutputWithType(ot uint8, accounts []*Address, s Script, amount Integer, seed []byte) {
	out := &Output{
		Type:   ot,
		Amount: amount,
		Script: s,
		Keys:   make([]*crypto.Key, 0),
	}

	if len(accounts) > 0 {
		r := crypto.NewKeyFromSeed(seed)
		out.Mask = r.Public()
		for _, a := range accounts {
			k := crypto.DeriveGhostPublicKey(&r, &a.PublicViewKey, &a.PublicSpendKey, uint64(len(tx.Outputs)))
			out.Keys = append(out.Keys, k)
		}
	}

	tx.Outputs = append(tx.Outputs, out)
}

func (tx *Transaction) AddScriptOutput(accounts []*Address, s Script, amount Integer, seed []byte) {
	tx.AddOutputWithType(OutputTypeScript, accounts, s, amount, seed)
}

func (tx *Transaction) AddRandomScriptOutput(accounts []*Address, s Script, amount Integer) {
	seed := make([]byte, 64)
	crypto.ReadRand(seed)
	tx.AddScriptOutput(accounts, s, amount, seed)
}
