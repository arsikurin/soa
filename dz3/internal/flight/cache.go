package flight

import (
	"context"
	"encoding/json"
	"errors"

	"flightbooking/internal/rpc/pb"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type cacheEntry struct {
	Revision int64           `json:"revision"`
	Data     json.RawMessage `json:"data"`
}

func (s *Service) cached(ctx context.Context, key string, result proto.Message) (bool, error) {
	if s.cache == nil {
		return false, nil
	}
	value, err := s.cache.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		s.logger.InfoContext(ctx, "cache_miss", "key", key, "reason", "absent")
		return false, nil
	}
	if err != nil {
		s.logger.WarnContext(ctx, "cache_error", "operation", "get", "key", key, "error", err)
		return false, nil
	}
	var entry cacheEntry
	if err := json.Unmarshal(value, &entry); err != nil {
		s.logger.WarnContext(ctx, "cache_error", "operation", "decode", "key", key, "error", err)
		return false, nil
	}
	var current int64
	if err := s.pool.QueryRow(ctx, "SELECT revision FROM cache_revision WHERE id=1").Scan(&current); err != nil {
		return false, s.databaseError(ctx, "cache_revision", err)
	}
	if entry.Revision != current {
		s.logger.InfoContext(ctx, "cache_miss", "key", key, "reason", "stale_revision", "cached_revision", entry.Revision, "current_revision", current)
		return false, nil
	}
	if err := protojson.Unmarshal(entry.Data, result); err != nil {
		proto.Reset(result)
		s.logger.WarnContext(ctx, "cache_error", "operation", "decode", "key", key, "error", err)
		return false, nil
	}
	s.logger.InfoContext(ctx, "cache_hit", "key", key, "revision", current)
	return true, nil
}

func (s *Service) fillCache(ctx context.Context, key string, revision int64, result proto.Message) {
	if s.cache == nil {
		return
	}
	data, err := protojson.Marshal(result)
	if err != nil {
		s.logger.WarnContext(ctx, "cache_error", "operation", "encode", "key", key, "error", err)
		return
	}
	value, err := json.Marshal(cacheEntry{Revision: revision, Data: data})
	if err != nil {
		s.logger.WarnContext(ctx, "cache_error", "operation", "encode", "key", key, "error", err)
		return
	}
	if err := s.cache.Set(ctx, key, value, s.ttl).Err(); err != nil {
		s.logger.WarnContext(ctx, "cache_error", "operation", "set", "key", key, "error", err)
	}
}

func (s *Service) invalidate(ctx context.Context, flight *pb.Flight) {
	if s.cache == nil {
		return
	}
	search := "search:" + flight.Origin + ":" + flight.Destination + ":"
	keys := []string{"flight:" + flight.Id, search + "all", search + flight.DepartureAt.AsTime().UTC().Format("2006-01-02")}
	if err := s.cache.Del(ctx, keys...).Err(); err != nil {
		s.logger.WarnContext(ctx, "cache_error", "operation", "invalidate", "flight_id", flight.Id, "error", err)
	}
}
