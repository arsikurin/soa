package auth

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"marketplace/internal/fault"
)

func (s *Service) LookupRole(ctx context.Context, id uuid.UUID) (string, error) {
	var role string
	err := s.db.QueryRow(ctx, "SELECT role FROM users WHERE id=$1", id).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fault.New(404, "USER_NOT_FOUND", "Пользователь не найден")
	}
	return role, err
}
