package auth

import (
	"errors"
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"marketplace/internal/identity"
	"time"
)

type Claims struct {
	Role      string `json:"role"`
	TokenType string `json:"token_type"`
	jwt.RegisteredClaims
}

type Tokens struct{ secret []byte }

func NewTokens(secret string) (*Tokens, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("JWT_SECRET must contain at least 32 bytes")
	}
	return &Tokens{secret: []byte(secret)}, nil
}
func (m *Tokens) Issue(p identity.Principal, kind string, ttl time.Duration) (string, *Claims, error) {
	now := time.Now()
	c := &Claims{Role: p.Role, TokenType: kind, RegisteredClaims: jwt.RegisteredClaims{
		Issuer: "marketplace", Subject: p.ID.String(), Audience: jwt.ClaimStrings{"marketplace-" + kind},
		ExpiresAt: jwt.NewNumericDate(now.Add(ttl)), IssuedAt: jwt.NewNumericDate(now), ID: uuid.NewString(),
	}}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(m.secret)
	return token, c, err
}
func (m *Tokens) Parse(raw, kind string) (*Claims, error) {
	c := new(Claims)
	_, err := jwt.ParseWithClaims(raw, c, func(t *jwt.Token) (any, error) { return m.secret, nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithIssuer("marketplace"),
		jwt.WithAudience("marketplace-"+kind), jwt.WithIssuedAt())
	if err != nil {
		return nil, err
	}
	id, err := uuid.Parse(c.Subject)
	if err != nil || id == uuid.Nil || c.TokenType != kind || (c.Role != "USER" && c.Role != "SELLER" && c.Role != "ADMIN") {
		return nil, errors.New("invalid token claims")
	}
	if _, err := uuid.Parse(c.ID); err != nil {
		return nil, errors.New("invalid token id")
	}
	return c, nil
}
