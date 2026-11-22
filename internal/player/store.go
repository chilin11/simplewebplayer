// Package player implements the persistent accounts, grants and HTTP API.
package player

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	_ "modernc.org/sqlite"
	"simplewebplayer/internal/media"
)

type Options struct {
	Media         media.Backend
	SecureCookies bool
}
type App struct {
	db        *sql.DB
	box       cipher.AEAD
	opts      Options
	hashSlots chan struct{}
	dummy     []byte
}

const schema = `
CREATE TABLE IF NOT EXISTS users(id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL COLLATE NOCASE UNIQUE, hash BLOB NOT NULL, role TEXT NOT NULL CHECK(role IN ('viewer','admin')), enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)));
CREATE TABLE IF NOT EXISTS sessions(hash BLOB PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, expires INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS session_users ON sessions(user_id);
CREATE INDEX IF NOT EXISTS session_expiry ON sessions(expires);
CREATE TABLE IF NOT EXISTS roots(id INTEGER PRIMARY KEY AUTOINCREMENT, label TEXT NOT NULL, path TEXT NOT NULL UNIQUE);
CREATE TABLE IF NOT EXISTS grants(user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, root_id INTEGER NOT NULL REFERENCES roots(id) ON DELETE CASCADE, PRIMARY KEY(user_id,root_id));
CREATE INDEX IF NOT EXISTS granted_roots ON grants(root_id);
CREATE TABLE IF NOT EXISTS nas(id INTEGER PRIMARY KEY CHECK(id=1),host TEXT NOT NULL,port INTEGER NOT NULL,share TEXT NOT NULL,username TEXT NOT NULL,domain TEXT NOT NULL,secret BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS attempts(key BLOB PRIMARY KEY, deadline INTEGER NOT NULL, n INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS attempt_expiry ON attempts(deadline);`

func privateFile(path string, flags int) (*os.File, error) {
	f, err := os.OpenFile(path, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err == nil {
		s := st.Sys().(*syscall.Stat_t)
		if !st.Mode().IsRegular() || s.Nlink != 1 || s.Uid != uint32(os.Geteuid()) {
			err = errors.New("data files must be regular, private and owned by the service account")
		}
	}
	if err == nil {
		err = f.Chmod(0600)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func loadKey(path, dbpath string) ([]byte, error) {
	f, err := privateFile(path, os.O_RDONLY)
	if err == nil {
		defer f.Close()
		key, e := io.ReadAll(io.LimitReader(f, 33))
		if e != nil {
			return nil, e
		}
		if len(key) != 32 {
			return nil, errors.New("invalid instance key; restore from backup")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if _, err = os.Lstat(dbpath); err == nil {
		return nil, errors.New("instance key missing; restore player.key from backup")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err = privateFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	key := make([]byte, 32)
	rand.Read(key)
	if _, err = f.Write(key); err != nil {
		return nil, err
	}
	if err = f.Sync(); err != nil {
		return nil, err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return key, d.Sync()
}

func Open(dataDir string, opts Options) (*App, error) {
	if dataDir == "" {
		return nil, errors.New("data directory is required")
	}
	dir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return nil, errors.New("data directory must be owned by the service account and not be a symlink")
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := privateFile(filepath.Join(dir, ".init.lock"), os.O_RDWR|os.O_CREATE)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "player.db")
	key, err := loadKey(filepath.Join(dir, "player.key"), path)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	box, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	f, err := privateFile(path, os.O_RDWR|os.O_CREATE)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: path}
	q := url.Values{"mode": {"rw"}, "_txlock": {"immediate"}, "_pragma": {"foreign_keys(1)", "busy_timeout(8000)"}}
	uri.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	app := &App{db: db, box: box, opts: opts, hashSlots: make(chan struct{}, 2)}
	if _, err = db.Exec("PRAGMA journal_mode=WAL"); err == nil {
		err = app.transaction(context.Background(), func(tx *sql.Tx) error { _, e := tx.Exec(schema); return e })
	}
	if err == nil {
		app.dummy, err = passwordHash(rand.Text())
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize player store: %w", err)
	}
	return app, nil
}
func (a *App) Close() error { return a.db.Close() }

// _txlock=immediate serializes read/modify/write decisions across processes. Keep
// transactions short: no request body reads, SMB I/O or streaming inside them.
func (a *App) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
