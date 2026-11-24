// Package passkeytest is a software authenticator for tests (M3-23): it answers the options of
// a registration or an assertion like a browser with a passkey would, with an ES256 key, "none"
// attestation and a signature counter the test controls.
package passkeytest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// Flags of the authenticator data (WebAuthn §6.1).
const (
	flagUP = 0x01
	flagUV = 0x04
	flagBE = 0x08
	flagBS = 0x10
	flagAT = 0x40
)

// Authenticator holds one credential.
type Authenticator struct {
	key    *ecdsa.PrivateKey
	ID     []byte
	User   []byte // the user handle of the registration
	RPID   string
	Count  uint32 // the next signature counter (0: a synced passkey that never counts)
	Verify bool   // user verification (PIN, biometrics)
	// Origin is what the browser puts into clientDataJSON.
	Origin string
	// Synced marks the credential as backed up (backup eligible and state).
	Synced bool
}

// New returns an authenticator for origin with user verification.
func New(origin string) *Authenticator {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	id := make([]byte, 32)
	_, _ = rand.Read(id)
	return &Authenticator{key: key, ID: id, Origin: origin, Verify: true, Count: 1}
}

var b64 = base64.RawURLEncoding

type creationOptions struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
		RP        struct {
			ID string `json:"id"`
		} `json:"rp"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	} `json:"publicKey"`
}

type requestOptions struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
		RPID      string `json:"rpId"`
	} `json:"publicKey"`
}

func (a *Authenticator) flags(attested bool) byte {
	f := byte(flagUP)
	if a.Verify {
		f |= flagUV
	}
	if a.Synced {
		f |= flagBE | flagBS
	}
	if attested {
		f |= flagAT
	}
	return f
}

func (a *Authenticator) authData(rpID string, attested bool) ([]byte, error) {
	hash := sha256.Sum256([]byte(rpID))
	out := append([]byte{}, hash[:]...)
	out = append(out, a.flags(attested))
	out = binary.BigEndian.AppendUint32(out, a.Count)
	if !attested {
		return out, nil
	}
	out = append(out, make([]byte, 16)...)                      // AAGUID: none
	out = binary.BigEndian.AppendUint16(out, uint16(len(a.ID))) //nolint:gosec // G115: 32 bytes
	out = append(out, a.ID...)
	pub, err := a.key.PublicKey.Bytes() // 0x04 || X || Y
	if err != nil {
		return nil, fmt.Errorf("public key: %w", err)
	}
	cose, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: pub[1:33], -3: pub[33:65]})
	if err != nil {
		return nil, fmt.Errorf("cose key: %w", err)
	}
	return append(out, cose...), nil
}

func (a *Authenticator) clientData(kind, challenge string) []byte {
	data, _ := json.Marshal(map[string]any{"type": kind, "challenge": challenge, "origin": a.Origin, "crossOrigin": false})
	return data
}

// Register answers the options of a registration (the JSON of PublicKeyCredentialCreationOptions
// wrapped in "publicKey") with the JSON a browser sends back.
func (a *Authenticator) Register(options []byte) ([]byte, error) {
	var o creationOptions
	if err := json.Unmarshal(options, &o); err != nil {
		return nil, fmt.Errorf("creation options: %w", err)
	}
	if o.PublicKey.Challenge == "" {
		return nil, errors.New("passkeytest: no challenge")
	}
	user, err := b64.DecodeString(o.PublicKey.User.ID)
	if err != nil {
		return nil, fmt.Errorf("user id: %w", err)
	}
	a.User, a.RPID = user, o.PublicKey.RP.ID
	auth, err := a.authData(a.RPID, true)
	if err != nil {
		return nil, err
	}
	att, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": auth})
	if err != nil {
		return nil, fmt.Errorf("attestation object: %w", err)
	}
	if a.Count > 0 {
		a.Count++
	}
	return json.Marshal(map[string]any{ //nolint:wrapcheck // test helper
		"id": b64.EncodeToString(a.ID), "rawId": b64.EncodeToString(a.ID), "type": "public-key",
		"authenticatorAttachment": "platform", "clientExtensionResults": map[string]any{},
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(a.clientData("webauthn.create", o.PublicKey.Challenge)),
			"attestationObject": b64.EncodeToString(att),
			"transports":        []string{"internal"},
		},
	})
}

// Assert answers the options of an assertion and counts up afterwards (unless the counter is 0).
func (a *Authenticator) Assert(options []byte) ([]byte, error) {
	var o requestOptions
	if err := json.Unmarshal(options, &o); err != nil {
		return nil, fmt.Errorf("request options: %w", err)
	}
	rpID := o.PublicKey.RPID
	if rpID == "" {
		rpID = a.RPID
	}
	auth, err := a.authData(rpID, false)
	if err != nil {
		return nil, err
	}
	client := a.clientData("webauthn.get", o.PublicKey.Challenge)
	hash := sha256.Sum256(client)
	digest := sha256.Sum256(append(append([]byte{}, auth...), hash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	if a.Count > 0 {
		a.Count++
	}
	return json.Marshal(map[string]any{ //nolint:wrapcheck // test helper
		"id": b64.EncodeToString(a.ID), "rawId": b64.EncodeToString(a.ID), "type": "public-key",
		"authenticatorAttachment": "platform", "clientExtensionResults": map[string]any{},
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(client),
			"authenticatorData": b64.EncodeToString(auth),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(a.User),
		},
	})
}
