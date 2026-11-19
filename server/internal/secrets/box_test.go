package secrets_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/secrets"
)

func key(t *testing.T, id uint32) secrets.Key {
	t.Helper()
	material := make([]byte, 32)
	if _, err := rand.Read(material); err != nil {
		t.Fatal(err)
	}
	k, err := secrets.NewKey(id, material)
	if err != nil {
		t.Fatalf("NewKey() error = %v", err)
	}
	return k
}

func box(t *testing.T, current secrets.Key, previous ...secrets.Key) *secrets.Box {
	t.Helper()
	b, err := secrets.NewBox(current, previous...)
	if err != nil {
		t.Fatalf("NewBox() error = %v", err)
	}
	return b
}

var ad = []byte("channel:0193f5a0-0000-7000-8000-000000000001")

func TestBox_RoundTrip(t *testing.T) {
	b := box(t, key(t, 1))
	plaintext := []byte("xoxb-slack-webhook-token")

	sealed, err := b.Seal(plaintext, ad)
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	if bytes.Contains(sealed, plaintext) {
		t.Fatal("ciphertext contains the plaintext")
	}
	opened, err := b.Open(sealed, ad)
	if err != nil || !bytes.Equal(opened, plaintext) {
		t.Errorf("Open() = %q, %v; want %q", opened, err, plaintext)
	}
}

func TestBox_SealUsesFreshNonces(t *testing.T) {
	b := box(t, key(t, 1))

	first, _ := b.Seal([]byte("same"), ad)
	second, _ := b.Seal([]byte("same"), ad)

	if bytes.Equal(first, second) {
		t.Error("sealing the same plaintext twice must produce different ciphertexts")
	}
}

func TestBox_RejectsOtherAssociatedData(t *testing.T) {
	b := box(t, key(t, 1))
	sealed, _ := b.Seal([]byte("secret"), ad)

	if _, err := b.Open(sealed, []byte("channel:another")); !errors.Is(err, secrets.ErrDecrypt) {
		t.Errorf("Open() with other associated data error = %v, want ErrDecrypt", err)
	}
}

func TestBox_DetectsEveryModifiedByte(t *testing.T) {
	b := box(t, key(t, 1))
	sealed, _ := b.Seal([]byte("secret value"), ad)

	for i := range sealed {
		tampered := bytes.Clone(sealed)
		tampered[i] ^= 0x01
		if _, err := b.Open(tampered, ad); err == nil {
			t.Fatalf("modification of byte %d was not detected", i)
		}
	}
}

func TestBox_RejectsTruncatedOrEmptyInput(t *testing.T) {
	b := box(t, key(t, 1))
	sealed, _ := b.Seal([]byte("secret"), ad)

	for _, input := range [][]byte{nil, {}, sealed[:1], sealed[:5], sealed[:20], sealed[:len(sealed)-1]} {
		if _, err := b.Open(input, ad); !errors.Is(err, secrets.ErrDecrypt) {
			t.Errorf("Open(%d bytes) error = %v, want ErrDecrypt", len(input), err)
		}
	}
}

func TestBox_KeyRotationKeepsOldValuesReadable(t *testing.T) {
	oldKey, newKey := key(t, 1), key(t, 2)
	sealedWithOld, _ := box(t, oldKey).Seal([]byte("legacy"), ad)

	rotated := box(t, newKey, oldKey)

	opened, err := rotated.Open(sealedWithOld, ad)
	if err != nil || string(opened) != "legacy" {
		t.Fatalf("Open(old ciphertext) = %q, %v", opened, err)
	}
	fresh, _ := rotated.Seal([]byte("new"), ad)
	if _, err := box(t, oldKey).Open(fresh, ad); err == nil {
		t.Error("new values must be sealed with the current key, not the old one")
	}
}

func TestBox_ResealMovesValuesToTheCurrentKey(t *testing.T) {
	oldKey, newKey := key(t, 1), key(t, 2)
	sealedWithOld, _ := box(t, oldKey).Seal([]byte("legacy"), ad)
	rotated := box(t, newKey, oldKey)

	resealed, err := rotated.Reseal(sealedWithOld, ad)
	if err != nil {
		t.Fatalf("Reseal() error = %v", err)
	}
	onlyNew := box(t, newKey)
	if opened, err := onlyNew.Open(resealed, ad); err != nil || string(opened) != "legacy" {
		t.Errorf("after Reseal the old key must no longer be needed: %q, %v", opened, err)
	}
	if id, err := secrets.KeyID(resealed); err != nil || id != 2 {
		t.Errorf("KeyID(resealed) = %d, %v; want 2", id, err)
	}
}

func TestBox_UnknownKeyIsRejected(t *testing.T) {
	sealed, _ := box(t, key(t, 7)).Seal([]byte("secret"), ad)

	if _, err := box(t, key(t, 1)).Open(sealed, ad); !errors.Is(err, secrets.ErrDecrypt) {
		t.Errorf("Open() with unknown key error = %v, want ErrDecrypt", err)
	}
}

func TestNewKey_Validation(t *testing.T) {
	if _, err := secrets.NewKey(1, make([]byte, 16)); !errors.Is(err, secrets.ErrInvalidKey) {
		t.Errorf("16-byte key error = %v, want ErrInvalidKey", err)
	}
	if _, err := secrets.NewKey(0, make([]byte, 32)); !errors.Is(err, secrets.ErrInvalidKey) {
		t.Errorf("key ID 0 error = %v, want ErrInvalidKey", err)
	}
	if _, err := secrets.NewKey(1, make([]byte, 32)); !errors.Is(err, secrets.ErrInvalidKey) {
		t.Errorf("all-zero key material error = %v, want ErrInvalidKey", err)
	}
}

func TestNewBox_RejectsDuplicateKeyIDs(t *testing.T) {
	if _, err := secrets.NewBox(key(t, 1), key(t, 1)); !errors.Is(err, secrets.ErrInvalidKey) {
		t.Errorf("duplicate key IDs error = %v, want ErrInvalidKey", err)
	}
}

func TestParseKeys_FirstKeyIsCurrent(t *testing.T) {
	k1 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	k2 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	text := "# master keys – newest first\n\n2:" + k2 + "\n1:" + k1 + "\n"

	b, err := secrets.ParseKeys(text)
	if err != nil {
		t.Fatalf("ParseKeys() error = %v", err)
	}
	sealed, _ := b.Seal([]byte("x"), ad)
	if id, _ := secrets.KeyID(sealed); id != 2 {
		t.Errorf("current key ID = %d, want 2 (first entry)", id)
	}
}

func TestParseKeys_Errors(t *testing.T) {
	valid := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	cases := map[string]string{
		"empty":         "",
		"only comments": "# nothing\n",
		"missing id":    valid,
		"bad id":        "x:" + valid,
		"bad base64":    "1:not-base64!!",
		"wrong length":  "1:" + base64.StdEncoding.EncodeToString(make([]byte, 31)),
		"duplicate ids": "1:" + valid + "\n1:" + valid,
	}
	for name, text := range cases {
		if _, err := secrets.ParseKeys(text); err == nil {
			t.Errorf("%s: ParseKeys() error = nil", name)
		}
	}
}

func TestParseKeys_ErrorDoesNotContainKeyMaterial(t *testing.T) {
	material := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 31))
	_, err := secrets.ParseKeys("1:" + material)
	if err == nil || strings.Contains(err.Error(), material) {
		t.Errorf("error %v must not echo key material", err)
	}
}

func TestKeyID_RejectsGarbage(t *testing.T) {
	if _, err := secrets.KeyID([]byte{1, 2}); !errors.Is(err, secrets.ErrDecrypt) {
		t.Errorf("KeyID(garbage) error = %v, want ErrDecrypt", err)
	}
}

func TestGenerateKey_ProducesUsableKeys(t *testing.T) {
	line, err := secrets.GenerateKeyLine(3)
	if err != nil {
		t.Fatalf("GenerateKeyLine() error = %v", err)
	}
	if !strings.HasPrefix(line, "3:") {
		t.Errorf("line = %q, want prefix 3:", line)
	}
	if _, err := secrets.ParseKeys(line); err != nil {
		t.Errorf("generated line is not parseable: %v", err)
	}
}

func TestNewBox_RejectsEmptyKeys(t *testing.T) {
	if _, err := secrets.NewBox(secrets.Key{}); !errors.Is(err, secrets.ErrInvalidKey) {
		t.Errorf("empty current key error = %v, want ErrInvalidKey", err)
	}
	if _, err := secrets.NewBox(key(t, 1), secrets.Key{}); !errors.Is(err, secrets.ErrInvalidKey) {
		t.Errorf("empty previous key error = %v, want ErrInvalidKey", err)
	}
}

func TestBox_ResealRejectsInvalidValues(t *testing.T) {
	if _, err := box(t, key(t, 1)).Reseal([]byte("garbage"), ad); !errors.Is(err, secrets.ErrDecrypt) {
		t.Errorf("Reseal(garbage) error = %v, want ErrDecrypt", err)
	}
}
