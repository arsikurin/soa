package auth

import (
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"marketplace/internal/identity"
	"testing"
	"time"
)

func TestTokensSeparateAccessFromRefresh(t *testing.T) {
	m, err := NewTokens("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	p := identity.Principal{ID: uuid.New(), Role: "USER"}
	access, _, err := m.Issue(p, "access", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Parse(access, "access")
	if err != nil || got.Subject != p.ID.String() || got.Role != "USER" {
		t.Fatalf("claims=%+v err=%v", got, err)
	}
	refresh, _, err := m.Issue(p, "refresh", 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Parse(refresh, "access"); err == nil {
		t.Fatal("refresh accepted as access")
	}
	if _, err := m.Parse(access, "refresh"); err == nil {
		t.Fatal("access accepted as refresh")
	}
}

func TestTokensRejectInvalidClaims(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	m, _ := NewTokens(secret)
	now := time.Now()
	good := jwt.MapClaims{"iss": "marketplace", "aud": "marketplace-access", "sub": uuid.NewString(), "role": "USER", "token_type": "access", "exp": now.Add(time.Minute).Unix(), "iat": now.Unix(), "jti": uuid.NewString()}
	for _, tc := range []struct {
		name, key string
		value     any
	}{
		{"expired", "exp", now.Add(-time.Minute).Unix()}, {"no expiry", "exp", nil},
		{"wrong audience", "aud", "marketplace-refresh"}, {"wrong issuer", "iss", "attacker"},
		{"bad subject", "sub", "not-a-uuid"}, {"bad role", "role", "ROOT"}, {"no role", "role", nil},
		{"wrong type", "token_type", "refresh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := jwt.MapClaims{}
			for k, v := range good {
				c[k] = v
			}
			c[tc.key] = tc.value
			token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte(secret))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.Parse(token, "access"); err == nil {
				t.Fatal("invalid claims accepted")
			}
		})
	}
	for _, method := range []jwt.SigningMethod{jwt.SigningMethodHS384, jwt.SigningMethodNone} {
		var key any = []byte(secret)
		if method == jwt.SigningMethodNone {
			key = jwt.UnsafeAllowNoneSignatureType
		}
		token, err := jwt.NewWithClaims(method, good).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Parse(token, "access"); err == nil {
			t.Fatal("unexpected signing method accepted")
		}
	}
	other, _ := NewTokens("abcdef0123456789abcdef0123456789")
	token, _, _ := other.Issue(identity.Principal{ID: uuid.New(), Role: "USER"}, "access", time.Minute)
	if _, err := m.Parse(token, "access"); err == nil {
		t.Fatal("wrong signature accepted")
	}
	if _, err := NewTokens("short"); err == nil {
		t.Fatal("weak secret accepted")
	}
}
