package auth

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"marketplace/internal/api"
	"os"
	"sync"
	"testing"
	"time"
)

func TestRegisterLoginAndRefreshRotation(t *testing.T) {
	dsn := os.Getenv("TEST_USERS_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_USERS_DATABASE_URL is required for PostgreSQL integration")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tokens, _ := NewTokens("0123456789abcdef0123456789abcdef")
	s := NewService(pool, tokens)
	email := uuid.NewString() + "@example.com"
	req := &api.RegisterRequest{Email: email, Password: "correct-horse-battery"}
	u, err := s.Register(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, "DELETE FROM refresh_tokens WHERE user_id=$1", u.ID); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id=$1", u.ID); err != nil {
			t.Error(err)
		}
	})
	if u.Role != api.RoleUSER {
		t.Fatalf("unexpected role %s", u.Role)
	}
	if _, err = s.Register(ctx, req); err == nil {
		t.Fatal("duplicate registration accepted")
	}
	if _, err = s.Login(ctx, &api.LoginRequest{Email: email, Password: "wrong-password"}); err == nil {
		t.Fatal("wrong password accepted")
	}
	pair, err := s.Login(ctx, &api.LoginRequest{Email: email, Password: req.Password})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := tokens.Parse(pair.AccessToken, "access")
	if err != nil {
		t.Fatal(err)
	}
	if ttl := time.Until(claims.ExpiresAt.Time); ttl < 14*time.Minute || ttl > 16*time.Minute {
		t.Fatalf("access TTL %v", ttl)
	}
	var hash string
	pool.QueryRow(ctx, "SELECT password_hash FROM users WHERE id=$1", u.ID).Scan(&hash)
	if hash == req.Password || hash == "" {
		t.Fatal("password not hashed")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := s.Refresh(ctx, &api.RefreshRequest{RefreshToken: pair.RefreshToken}); results <- err })
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("refresh rotation successes=%d, want1", successes)
	}
	if _, err = s.Refresh(ctx, &api.RefreshRequest{RefreshToken: pair.AccessToken}); err == nil {
		t.Fatal("access accepted as refresh")
	}
}
