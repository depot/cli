package ssh

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"

	xssh "golang.org/x/crypto/ssh"
)

func NewKeyPair() (PublicKey, PrivateKey) {
	// Generate Ed25519 key pair
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}

	// Encode public key in OpenSSH format
	pubKey, err := xssh.NewPublicKey(pub)
	if err != nil {
		panic(err)
	}
	pubBytes := bytes.TrimSpace(xssh.MarshalAuthorizedKey(pubKey))

	// Encode private key in OpenSSH format
	privKey := marshalED25519PrivateKey(priv)
	privPemKey := &pem.Block{
		Type:  "OPENSSH PRIVATE KEY",
		Bytes: privKey,
	}
	privBytes := pem.EncodeToMemory(privPemKey)

	return PublicKey(pubBytes), PrivateKey(privBytes)
}
