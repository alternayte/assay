// Package secrets stores credentials encrypted with AES-256-GCM. No route
// returns a secret value.
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound means that no secret has the name.
var ErrNotFound = errors.New("secrets: not found")

// Box encrypts and decrypts with one key.
type Box struct {
	aead    cipher.AEAD
	version int32
}

// NewBox returns a box for a 32-byte key.
func NewBox(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("secrets: the key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead, version: KeyVersion(key)}, nil
}

// KeyVersion identifies a key without revealing it: the first four bytes of
// its SHA-256 hash.
func KeyVersion(key []byte) int32 {
	h := sha256.Sum256(key)
	return int32(binary.BigEndian.Uint32(h[:4]) & 0x7fffffff) //nolint:gosec // The mask keeps the value in range.
}

func (b *Box) seal(name string, plaintext []byte) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	// The name is additional data, so a ciphertext does not decrypt under
	// another name.
	return b.aead.Seal(nil, nonce, plaintext, []byte(name)), nonce, nil
}

func (b *Box) open(name string, version int32, ciphertext, nonce []byte) ([]byte, error) {
	if version != b.version {
		return nil, fmt.Errorf("secrets: %s is encrypted with another key", name)
	}
	p, err := b.aead.Open(nil, nonce, ciphertext, []byte(name))
	if err != nil {
		return nil, fmt.Errorf("secrets: %s does not decrypt with this key", name)
	}
	return p, nil
}

// Store reads and writes secrets.
type Store struct {
	Pool *pgxpool.Pool
	Box  *Box
}

// Put encrypts and stores a secret.
func (s *Store) Put(ctx context.Context, db DBTX, name string, value []byte) error {
	ct, nonce, err := s.Box.seal(name, value)
	if err != nil {
		return err
	}
	return New(db).Upsert(ctx, UpsertParams{Name: name, Ciphertext: ct, Nonce: nonce, KeyVersion: s.Box.version})
}

// Get decrypts a secret. It returns ErrNotFound when no secret has the name.
func (s *Store) Get(ctx context.Context, name string) ([]byte, error) {
	r, err := New(s.Pool).Get(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.Box.open(name, r.KeyVersion, r.Ciphertext, r.Nonce)
}

// Delete removes a secret.
func (s *Store) Delete(ctx context.Context, db DBTX, name string) error {
	_, err := New(db).Delete(ctx, name)
	return err
}

// Rekey encrypts every secret again with the key of next, in one
// transaction. It fails and changes nothing when a secret does not decrypt
// with the current key.
func Rekey(ctx context.Context, pool *pgxpool.Pool, current, next *Box) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := New(tx)
		rows, err := q.LockAll(ctx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.KeyVersion == next.version {
				continue
			}
			p, err := current.open(r.Name, r.KeyVersion, r.Ciphertext, r.Nonce)
			if err != nil {
				return err
			}
			ct, nonce, err := next.seal(r.Name, p)
			if err != nil {
				return err
			}
			if err := q.Upsert(ctx, UpsertParams{Name: r.Name, Ciphertext: ct, Nonce: nonce, KeyVersion: next.version}); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}
