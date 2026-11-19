// Package secrets encrypts secrets at rest (KONZEPT §18.5): channel credentials, module
// credentials, TOTP seeds and the agent CA key.
//
// Format of a sealed value: version (1 byte) | key ID (4 bytes, big endian) | nonce (12 bytes) |
// AES-256-GCM ciphertext and tag. The associated data binds a value to its context (e.g.
// "channel:<id>"), so a ciphertext cannot be moved to another record. Decryption errors are
// deliberately indistinguishable (ErrDecrypt).
package secrets

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	formatVersion = 1
	keySize       = 32
	nonceSize     = 12
	headerSize    = 1 + 4 + nonceSize
	tagSize       = 16
)

var (
	// ErrInvalidKey means a key or key set is malformed.
	ErrInvalidKey = errors.New("invalid secrets key")
	// ErrDecrypt means a sealed value could not be opened (wrong key, context or modified data).
	ErrDecrypt = errors.New("cannot decrypt secret")
)

// Key is a master key with its ID. The ID is stored in every sealed value.
type Key struct {
	id   uint32
	aead cipher.AEAD
}

// NewKey creates a key from 32 bytes of random key material. ID 0 is reserved.
func NewKey(id uint32, material []byte) (Key, error) {
	if id == 0 {
		return Key{}, fmt.Errorf("%w: key ID must not be 0", ErrInvalidKey)
	}
	if len(material) != keySize {
		return Key{}, fmt.Errorf("%w: key %d must be %d bytes, got %d", ErrInvalidKey, id, keySize, len(material))
	}
	if subtle.ConstantTimeCompare(material, make([]byte, keySize)) == 1 {
		return Key{}, fmt.Errorf("%w: key %d is all zeros", ErrInvalidKey, id)
	}
	block, err := aes.NewCipher(material)
	if err != nil {
		return Key{}, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Key{}, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}
	return Key{id: id, aead: aead}, nil
}

// Box seals with the current key and opens with the current or any previous key.
type Box struct {
	current Key
	keys    map[uint32]Key
}

// NewBox creates a Box. previous keys stay readable during a key rotation.
func NewBox(current Key, previous ...Key) (*Box, error) {
	if current.aead == nil {
		return nil, fmt.Errorf("%w: no current key", ErrInvalidKey)
	}
	keys := map[uint32]Key{current.id: current}
	for _, k := range previous {
		if k.aead == nil {
			return nil, fmt.Errorf("%w: empty previous key", ErrInvalidKey)
		}
		if _, exists := keys[k.id]; exists {
			return nil, fmt.Errorf("%w: duplicate key ID %d", ErrInvalidKey, k.id)
		}
		keys[k.id] = k
	}
	return &Box{current: current, keys: keys}, nil
}

// Seal encrypts plaintext with the current key, bound to associatedData.
func (b *Box) Seal(plaintext, associatedData []byte) ([]byte, error) {
	out := make([]byte, headerSize, headerSize+len(plaintext)+tagSize)
	out[0] = formatVersion
	binary.BigEndian.PutUint32(out[1:5], b.current.id)
	nonce := out[5:headerSize]
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	return b.current.aead.Seal(out, nonce, plaintext, b.additional(out[:5], associatedData)), nil
}

// Open decrypts a value sealed by Seal with the same associated data.
func (b *Box) Open(sealed, associatedData []byte) ([]byte, error) {
	id, err := KeyID(sealed)
	if err != nil {
		return nil, err
	}
	key, ok := b.keys[id]
	if !ok || len(sealed) < headerSize+tagSize {
		return nil, ErrDecrypt
	}
	plaintext, err := key.aead.Open(nil, sealed[5:headerSize], sealed[headerSize:], b.additional(sealed[:5], associatedData))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

// Reseal re-encrypts a value with the current key (key rotation job).
func (b *Box) Reseal(sealed, associatedData []byte) ([]byte, error) {
	plaintext, err := b.Open(sealed, associatedData)
	if err != nil {
		return nil, err
	}
	return b.Seal(plaintext, associatedData)
}

// additional authenticates the header (version and key ID) together with the caller's context.
func (b *Box) additional(header, associatedData []byte) []byte {
	return append(bytes.Clone(header), associatedData...)
}

// KeyID returns the ID of the key a value was sealed with.
func KeyID(sealed []byte) (uint32, error) {
	if len(sealed) < headerSize || sealed[0] != formatVersion {
		return 0, ErrDecrypt
	}
	return binary.BigEndian.Uint32(sealed[1:5]), nil
}

// ParseKeys reads a key file: one "<id>:<base64 key>" per line, newest (current) key first;
// empty lines and lines starting with # are ignored. Errors never contain key material.
func ParseKeys(text string) (*Box, error) {
	var keys []Key
	scanner := bufio.NewScanner(strings.NewReader(text))
	line := 0
	for scanner.Scan() {
		line++
		entry := strings.TrimSpace(scanner.Text())
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		idText, encoded, found := strings.Cut(entry, ":")
		if !found {
			return nil, fmt.Errorf("%w: line %d: want <id>:<base64 key>", ErrInvalidKey, line)
		}
		id, err := strconv.ParseUint(idText, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("%w: line %d: invalid key ID", ErrInvalidKey, line)
		}
		material, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("%w: line %d: key is not valid base64", ErrInvalidKey, line)
		}
		k, err := NewKey(uint32(id), material)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		keys = append(keys, k)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read keys: %w", err)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: no keys found", ErrInvalidKey)
	}
	return NewBox(keys[0], keys[1:]...)
}

// GenerateKeyLine returns a new random key as a key-file line "<id>:<base64 key>".
func GenerateKeyLine(id uint32) (string, error) {
	material := make([]byte, keySize)
	if _, err := rand.Read(material); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	return strconv.FormatUint(uint64(id), 10) + ":" + base64.StdEncoding.EncodeToString(material), nil
}
