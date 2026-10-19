package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
	"marketplace/internal/api"
	"marketplace/internal/fault"
	"marketplace/internal/identity"
)

type Service struct {
	db     *pgxpool.Pool
	tokens *Tokens
}

func NewService(db *pgxpool.Pool, tokens *Tokens) *Service { return &Service{db: db, tokens: tokens} }

func (s *Service) Register(ctx context.Context, r *api.RegisterRequest) (*api.UserResponse, error) {
	role := string(r.Role.Or(api.RegisterRequestRoleUSER))
	if role != "USER" && role != "SELLER" {
		return nil, fault.Denied()
	}
	if len(r.Password) > 72 {
		return nil, fault.Validation("password", "Пароль должен занимать не более 72 байт UTF-8")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(r.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	u := &api.UserResponse{ID: uuid.New(), Email: strings.ToLower(r.Email), Role: api.Role(role)}
	err = s.db.QueryRow(ctx, `INSERT INTO users(id,email,password_hash,role) VALUES($1,$2,$3,$4) RETURNING created_at,updated_at`, u.ID, u.Email, string(hash), role).Scan(&u.CreatedAt, &u.UpdatedAt)
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23505" {
		return nil, fault.New(409, "EMAIL_ALREADY_EXISTS", "Этот адрес уже зарегистрирован")
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}
func (s *Service) Login(ctx context.Context, r *api.LoginRequest) (*api.TokenPair, error) {
	var p identity.Principal
	var hash string
	err := s.db.QueryRow(ctx, "SELECT id,role,password_hash FROM users WHERE email=$1", strings.ToLower(r.Email)).Scan(&p.ID, &p.Role, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fault.New(401, "INVALID_CREDENTIALS", "Неверный адрес или пароль")
	}
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(r.Password)) != nil {
		return nil, fault.New(401, "INVALID_CREDENTIALS", "Неверный адрес или пароль")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	pair, err := s.issuePair(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return pair, nil
}
func tokenHash(raw string) string { h := sha256.Sum256([]byte(raw)); return hex.EncodeToString(h[:]) }
func (s *Service) issuePair(ctx context.Context, tx pgx.Tx, p identity.Principal) (*api.TokenPair, error) {
	access, _, err := s.tokens.Issue(p, "access", 15*time.Minute)
	if err != nil {
		return nil, err
	}
	refresh, claims, err := s.tokens.Issue(p, "refresh", 7*24*time.Hour)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO refresh_tokens(id,user_id,token_hash,expires_at) VALUES($1,$2,$3,$4)`, claims.ID, p.ID, tokenHash(refresh), claims.ExpiresAt.Time)
	if err != nil {
		return nil, err
	}
	return &api.TokenPair{AccessToken: access, RefreshToken: refresh, TokenType: api.TokenPairTokenTypeBearer, ExpiresIn: 900}, nil
}
func (s *Service) Refresh(ctx context.Context, r *api.RefreshRequest) (*api.TokenPair, error) {
	invalid := func() error {
		return fault.New(401, "REFRESH_TOKEN_INVALID", "Refresh token недействителен")
	}
	c, err := s.tokens.Parse(r.RefreshToken, "refresh")
	if err != nil {
		return nil, invalid()
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var p identity.Principal
	err = tx.QueryRow(ctx, `SELECT u.id,u.role FROM refresh_tokens t JOIN users u ON u.id=t.user_id
 WHERE t.id=$1 AND t.token_hash=$2 AND t.user_id=$3 AND t.revoked_at IS NULL AND t.expires_at>clock_timestamp() FOR UPDATE OF t`, c.ID, tokenHash(r.RefreshToken), c.Subject).Scan(&p.ID, &p.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, invalid()
	}
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, "UPDATE refresh_tokens SET revoked_at=clock_timestamp() WHERE id=$1", c.ID)
	if err != nil {
		return nil, err
	}
	pair, err := s.issuePair(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return pair, nil
}

func (s *Service) BootstrapAdmin(ctx context.Context, email, password string) error {
	if email == "" && password == "" {
		return nil
	}
	if email == "" || len(password) < 8 || len(password) > 72 {
		return fmt.Errorf("bootstrap administrator requires email and password of 8–72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO users(id,email,password_hash,role) VALUES($1,$2,$3,'ADMIN') ON CONFLICT(email) DO NOTHING`, uuid.New(), strings.ToLower(email), string(hash))
	if err != nil {
		return err
	}
	var role string
	if err = s.db.QueryRow(ctx, "SELECT role FROM users WHERE email=$1", strings.ToLower(email)).Scan(&role); err != nil {
		return err
	}
	if role != "ADMIN" {
		return fmt.Errorf("bootstrap email belongs to a non-administrator")
	}
	return nil
}
