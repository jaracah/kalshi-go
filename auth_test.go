package kalshi

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"testing"
	"time"
)

// testKey is generated once per test binary — RSA generation is the slow part.
var testKey = func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
}()

func testKeyPEM(t *testing.T) string {
	t.Helper()
	return string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(testKey),
	}))
}

func testKeyPKCS8PEM(t *testing.T) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(testKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestSignerSignsSpecMessage(t *testing.T) {
	s, err := NewSigner("key-id-1", testKeyPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return fixed }

	// Query params must NOT be part of the signed message (the spec's rule).
	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/trade-api/v2/portfolio/orders?limit=5", nil)
	if err := s.sign(req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("KALSHI-ACCESS-KEY"); got != "key-id-1" {
		t.Errorf("KALSHI-ACCESS-KEY = %q", got)
	}
	ts := req.Header.Get("KALSHI-ACCESS-TIMESTAMP")
	if want := "1784203200000"; ts != want {
		t.Errorf("KALSHI-ACCESS-TIMESTAMP = %q, want %q (ms)", ts, want)
	}
	sig, err := base64.StdEncoding.DecodeString(req.Header.Get("KALSHI-ACCESS-SIGNATURE"))
	if err != nil {
		t.Fatalf("signature not base64: %v", err)
	}
	msg := ts + "GET" + "/trade-api/v2/portfolio/orders"
	digest := sha256.Sum256([]byte(msg))
	if err := rsa.VerifyPSS(&testKey.PublicKey, crypto.SHA256, digest[:], sig, &rsa.PSSOptions{
		SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: crypto.SHA256,
	}); err != nil {
		t.Errorf("signature does not verify over the spec message %q: %v", msg, err)
	}
}

func TestNewSignerParsesBothPEMForms(t *testing.T) {
	if _, err := NewSigner("k", testKeyPEM(t)); err != nil {
		t.Errorf("PKCS#1: %v", err)
	}
	if _, err := NewSigner("k", testKeyPKCS8PEM(t)); err != nil {
		t.Errorf("PKCS#8: %v", err)
	}
	if _, err := NewSigner("k", "not a key"); err == nil {
		t.Error("garbage PEM should error")
	}
	if _, err := NewSigner("", testKeyPEM(t)); err == nil {
		t.Error("empty key id should error")
	}
}
