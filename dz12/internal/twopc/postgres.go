package twopc

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const Timeout = 10 * time.Second

func GID(domain, id string) (string, error) {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id || (domain != "catalog" && domain != "orders") {
		return "", fmt.Errorf("invalid transaction identifier")
	}
	return "marketplace:" + domain + ":" + id, nil
}

func lockKey(gid string) int64 {
	h := sha256.Sum256([]byte(gid))
	return int64(binary.BigEndian.Uint64(h[:8]))
}

func Lock(ctx context.Context, c *pgx.Conn, domain, id string, try bool) (bool, error) {
	gid, err := GID(domain, id)
	if err != nil {
		return false, err
	}
	if try {
		var ok bool
		err = c.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockKey(gid)).Scan(&ok)
		return ok, err
	}
	_, err = c.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey(gid))
	return err == nil, err
}

func Release(c *pgxpool.Conn, domain, id string) {
	ReleaseContext(context.Background(), c, domain, id)
}

func ReleaseContext(parent context.Context, c *pgxpool.Conn, domain, id string) {
	ctx, cancel := context.WithTimeout(parent, 500*time.Millisecond)
	defer cancel()
	gid, err := GID(domain, id)
	if err == nil {
		_, err = c.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockKey(gid))
	}
	if err != nil || c.Conn().PgConn().TxStatus() != 'I' {
		_ = c.Conn().Close(ctx)
	}
	c.Release()
}

func Begin(ctx context.Context, c *pgx.Conn) (pgx.Tx, error) {
	tx, err := c.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "SET LOCAL lock_timeout='5s'; SET LOCAL statement_timeout='10s'"); err != nil {
		RollbackContext(ctx, tx)
		return nil, err
	}
	return tx, nil
}
func Rollback(tx pgx.Tx) {
	RollbackContext(context.Background(), tx)
}
func RollbackContext(parent context.Context, tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(parent, 500*time.Millisecond)
	defer cancel()
	_ = tx.Rollback(ctx)
}
func Prepare(ctx context.Context, tx pgx.Tx, domain, id string) error {
	gid, err := GID(domain, id)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "PREPARE TRANSACTION '"+gid+"'")
	return err
}
func Prepared(ctx context.Context, c *pgx.Conn, domain, id string) (bool, error) {
	gid, err := GID(domain, id)
	if err != nil {
		return false, err
	}
	var exists bool
	err = c.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_prepared_xacts WHERE gid=$1 AND database=current_database())", gid).Scan(&exists)
	return exists, err
}
func Receipt(ctx context.Context, c *pgx.Conn, domain, id string) (bool, error) {
	if _, err := GID(domain, id); err != nil {
		return false, err
	}
	table := "transaction_receipts"
	if domain == "catalog" {
		table = "reservation_receipts"
	}
	var exists bool
	err := c.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE id=$1)", id).Scan(&exists)
	return exists, err
}

func Resolve(ctx context.Context, c *pgx.Conn, domain, id string, commit bool) error {
	gid, err := GID(domain, id)
	if err != nil {
		return err
	}
	receipt, err := Receipt(ctx, c, domain, id)
	if err != nil {
		return err
	}
	if receipt {
		if commit {
			return nil
		}
		return errors.New("cannot abort a committed transaction")
	}
	prepared, err := Prepared(ctx, c, domain, id)
	if err != nil {
		return err
	}
	if prepared {
		command := "ROLLBACK PREPARED '"
		if commit {
			command = "COMMIT PREPARED '"
		}
		if _, err = c.Exec(ctx, command+gid+"'"); err != nil {
			return err
		}
	}
	if !commit {
		return nil
	}
	receipt, err = Receipt(ctx, c, domain, id)
	if err != nil {
		return err
	}
	if !receipt {
		return errors.New("commit cannot be confirmed: prepared transaction and receipt are missing")
	}
	return nil
}
