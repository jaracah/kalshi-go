// Request signing for Kalshi's authenticated endpoints, per the published
// spec (docs.kalshi.com "API Keys", verified 2026-07-16): the message
// timestampMs + METHOD + path — where path INCLUDES the /trade-api/v2 prefix
// and EXCLUDES query parameters — is signed with RSA-PSS (SHA-256, salt
// length = digest length) and sent base64-encoded in three headers.

package kalshi

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Signer holds an API key id and its RSA private key, and signs requests in
// place. Safe for concurrent use.
type Signer struct {
	keyID string
	key   *rsa.PrivateKey
	now   func() time.Time // test seam
}

// NewSigner parses pemKey (PKCS#1 "RSA PRIVATE KEY" or PKCS#8 "PRIVATE KEY")
// and returns a Signer for keyID. The key material never leaves the process;
// callers typically load it from the environment or a secrets store.
func NewSigner(keyID, pemKey string) (*Signer, error) {
	if keyID == "" {
		return nil, errors.New("kalshi: empty API key id")
	}
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return nil, errors.New("kalshi: private key is not PEM")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = k
	} else if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("kalshi: PKCS#8 key is %T, want RSA", k)
		}
		key = rk
	} else {
		return nil, errors.New("kalshi: private key parses as neither PKCS#1 nor PKCS#8")
	}
	return &Signer{keyID: keyID, key: key, now: time.Now}, nil
}

// sign adds the three KALSHI-ACCESS-* headers to req. The signed path is
// req.URL.Path verbatim — the client builds URLs with the /trade-api/v2
// prefix in the path, and Go's URL parsing keeps query params out of Path,
// which is exactly the spec's "path without query parameters".
func (s *Signer) sign(req *http.Request) error {
	ts := strconv.FormatInt(s.now().UnixMilli(), 10)
	msg := ts + req.Method + req.URL.Path
	digest := sha256.Sum256([]byte(msg))
	sig, err := rsa.SignPSS(rand.Reader, s.key, crypto.SHA256, digest[:], &rsa.PSSOptions{
		SaltLength: rsa.PSSSaltLengthEqualsHash,
		Hash:       crypto.SHA256,
	})
	if err != nil {
		return fmt.Errorf("kalshi: sign request: %w", err)
	}
	req.Header.Set("KALSHI-ACCESS-KEY", s.keyID)
	req.Header.Set("KALSHI-ACCESS-TIMESTAMP", ts)
	req.Header.Set("KALSHI-ACCESS-SIGNATURE", base64.StdEncoding.EncodeToString(sig))
	return nil
}
