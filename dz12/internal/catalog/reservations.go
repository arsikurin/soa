package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"marketplace/internal/api"
	"marketplace/internal/fault"
	"marketplace/internal/twopc"
)

func reservationConflict(message string) error {
	return fault.New(409, "TRANSACTION_CONFLICT", message)
}

func (s *Service) PrepareReservation(ctx context.Context, id string, old []api.OrderItemResponse, next []api.OrderItemRequest) ([]api.OrderItemResponse, error) {
	if _, err := twopc.GID("catalog", id); err != nil {
		return nil, err
	}
	if err := validateReservation(old, next); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, twopc.Timeout)
	defer cancel()
	payload, err := json.Marshal(struct {
		Old  []api.OrderItemResponse
		Next []api.OrderItemRequest
	}{old, next})
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(payload)
	digest := hex.EncodeToString(hash[:])
	c, err := s.db.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer twopc.Release(c, "catalog", id)
	if _, err = twopc.Lock(ctx, c.Conn(), "catalog", id, false); err != nil {
		return nil, err
	}
	if _, err = c.Exec(ctx, "INSERT INTO reservation_journal(id,digest,payload,state) VALUES($1,$2,$3,'PREPARING') ON CONFLICT DO NOTHING", id, digest, payload); err != nil {
		return nil, err
	}
	var savedDigest *string
	var state string
	var result []byte
	if err = c.QueryRow(ctx, "SELECT digest,state,result FROM reservation_journal WHERE id=$1", id).Scan(&savedDigest, &state, &result); err != nil {
		return nil, err
	}
	if state == "ABORT" {
		return nil, reservationConflict("Транзакция отменена")
	}
	if savedDigest == nil || *savedDigest != digest {
		return nil, reservationConflict("Идентификатор транзакции использован с другими данными")
	}
	var receipt []byte
	err = c.QueryRow(ctx, "SELECT result FROM reservation_receipts WHERE id=$1", id).Scan(&receipt)
	if err == nil {
		return decodeItems(receipt)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	prepared, err := twopc.Prepared(ctx, c.Conn(), "catalog", id)
	if err != nil {
		return nil, err
	}
	if prepared {
		if len(result) == 0 {
			return nil, fmt.Errorf("prepared reservation snapshot awaits resolution")
		}
		return decodeItems(result)
	}
	if state != "PREPARING" {
		return nil, fmt.Errorf("reservation has no prepared transaction or commit receipt")
	}
	tx, err := twopc.Begin(ctx, c.Conn())
	if err != nil {
		return nil, err
	}
	didPrepare := false
	defer func() {
		if !didPrepare {
			twopc.Rollback(tx)
		}
	}()
	ids := productIDs(next, old)
	products, err := lockProducts(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	if err = restoreItems(products, old); err != nil {
		return nil, err
	}
	items, _, err := reserveItems(products, next)
	if err != nil {
		return nil, err
	}
	if err = saveStock(ctx, tx, ids, products); err != nil {
		return nil, err
	}
	result, err = json.Marshal(items)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO reservation_receipts(id,result) VALUES($1,$2)", id, result); err != nil {
		return nil, err
	}
	if err = twopc.Prepare(ctx, tx, "catalog", id); err != nil {
		return nil, err
	}
	didPrepare = true
	if _, err = c.Exec(ctx, "UPDATE reservation_journal SET result=$2,state='PREPARED' WHERE id=$1", id, result); err != nil {
		return nil, err
	}
	return items, nil
}

func decodeItems(data []byte) ([]api.OrderItemResponse, error) {
	var items []api.OrderItemResponse
	err := json.Unmarshal(data, &items)
	return items, err
}

func validateReservation(old []api.OrderItemResponse, next []api.OrderItemRequest) error {
	if len(old) > 50 || len(next) > 50 {
		return fault.Validation("items", "Количество позиций должно быть не больше 50")
	}
	for _, set := range [][]api.OrderItemRequest{next, oldRequests(old)} {
		seen := map[uuid.UUID]bool{}
		for _, item := range set {
			if seen[item.ProductID] || item.Quantity < 1 || item.Quantity > 999 {
				return fault.Validation("items", "Некорректные позиции резерва")
			}
			seen[item.ProductID] = true
		}
	}
	return nil
}
func oldRequests(old []api.OrderItemResponse) []api.OrderItemRequest {
	out := make([]api.OrderItemRequest, len(old))
	for i, item := range old {
		out[i] = api.OrderItemRequest{ProductID: item.ProductID, Quantity: item.Quantity}
	}
	return out
}

func (s *Service) ResolveReservation(ctx context.Context, id string, commit bool) error {
	if _, err := twopc.GID("catalog", id); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, twopc.Timeout)
	defer cancel()
	c, err := s.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer twopc.Release(c, "catalog", id)
	if _, err = twopc.Lock(ctx, c.Conn(), "catalog", id, false); err != nil {
		return err
	}
	if !commit {
		if _, err = c.Exec(ctx, "INSERT INTO reservation_journal(id,state) VALUES($1,'ABORT') ON CONFLICT DO NOTHING", id); err != nil {
			return err
		}
	}
	var state string
	if err = c.QueryRow(ctx, "SELECT state FROM reservation_journal WHERE id=$1", id).Scan(&state); err != nil {
		return err
	}
	decision := "ABORT"
	if commit {
		decision = "COMMIT"
	}
	if (state == "COMMIT" || state == "ABORT") && state != decision {
		return reservationConflict("Решение транзакции уже принято")
	}
	if _, err = c.Exec(ctx, "UPDATE reservation_journal SET state=$2 WHERE id=$1", id, decision); err != nil {
		return err
	}
	if err = twopc.Resolve(ctx, c.Conn(), "catalog", id, commit); err != nil {
		return err
	}
	if commit {
		_, err = c.Exec(ctx, "UPDATE reservation_journal SET result=(SELECT result FROM reservation_receipts WHERE id=$1) WHERE id=$1", id)
	}
	return err
}
