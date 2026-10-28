package sqlitelocal

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
)

// Cipher reports the SQLCipher build the binary links: its version and the crypto provider its encryption uses, as
// SQLCipher itself names them (commoncrypto, openssl). It asks a throwaway in-memory database, keyed with a random
// key, so no file is opened.
func Cipher(ctx context.Context) (version, provider string, err error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return "", "", fmt.Errorf("sqlitelocal: generating a throwaway key: %w", err)
	}
	conn, err := openWithKeyMaterial(":memory:", keySpec(key), 0, true)
	if err != nil {
		return "", "", fmt.Errorf("sqlitelocal: opening a throwaway database: %w", err)
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
	}()

	if version, err = pragmaText(ctx, conn, "PRAGMA cipher_version"); err != nil {
		return "", "", err
	}
	if provider, err = pragmaText(ctx, conn, "PRAGMA cipher_provider"); err != nil {
		return "", "", err
	}
	return version, provider, nil
}

// pragmaText runs pragma on conn, internal statements allowed, and returns the text of its first row's first column.
func pragmaText(ctx context.Context, conn *Conn, pragma string) (string, error) {
	var text string
	err := conn.WithInternal(func() error {
		rows, err := conn.Query(ctx, pragma)
		if err != nil {
			return err
		}
		if !rows.Next() {
			return errors.Join(errors.New("no result"), rows.Err(), rows.Close())
		}
		return errors.Join(rows.Scan(&text), rows.Close())
	})
	if err != nil {
		return "", fmt.Errorf("sqlitelocal: %s: %w", pragma, err)
	}
	return text, nil
}
