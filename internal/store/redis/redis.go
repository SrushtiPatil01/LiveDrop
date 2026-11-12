package redisstore

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/redis/go-redis/v9"

	"github.com/SrushtiPatil01/LiveDrop/internal/core/model"
)

var (
	ErrStockMissing = errors.New("stock cache miss")
	ErrSoldOut      = model.ErrSoldOut
)

var reserveScript = redis.NewScript(`
local quantity = tonumber(ARGV[1])
if quantity == nil or quantity <= 0 then return -3 end
local stock = tonumber(redis.call('GET', KEYS[1]))
if stock == nil then return -1 end
if stock < quantity then return -2 end
return redis.call('DECRBY', KEYS[1], quantity)
`)

var rateLimitScript = redis.NewScript(`
local rate = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local redisTime = redis.call('TIME')
local now = tonumber(redisTime[1]) + tonumber(redisTime[2]) / 1000000
local values = redis.call('HMGET', KEYS[1], 'tokens', 'updated')
local tokens = tonumber(values[1]) or burst
local updated = tonumber(values[2]) or now
tokens = math.min(burst, tokens + math.max(0, now - updated) * rate)
local allowed = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
end
redis.call('HSET', KEYS[1], 'tokens', tokens, 'updated', now)
redis.call('EXPIRE', KEYS[1], math.max(1, math.ceil(burst / rate * 2)))
return allowed
`)

var viewerCountScript = redis.NewScript(`
local count = redis.call('INCRBY', KEYS[1], ARGV[1])
if count < 0 then
  redis.call('SET', KEYS[1], 0)
  return 0
end
return count
`)

type Store struct {
	client *redis.Client
}

func Open(ctx context.Context, address, password string, db int, useTLS bool) (*Store, error) {
	options := &redis.Options{
		Addr:         address,
		Password:     password,
		DB:           db,
		PoolSize:     100,
		MinIdleConns: 5,
	}
	if useTLS {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("parse Redis address for TLS: %w", err)
		}
		options.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	}
	client := redis.NewClient(options)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return &Store{client: client}, nil
}

func (s *Store) Close() error { return s.client.Close() }

func (s *Store) Ping(ctx context.Context) error {
	if err := s.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("ping redis: %w", err)
	}
	return nil
}

func (s *Store) SeedStock(ctx context.Context, productID string, quantity int64) (int64, error) {
	key := stockKey(productID)
	if err := s.client.SetNX(ctx, key, quantity, 0).Err(); err != nil {
		return 0, fmt.Errorf("seed stock cache: %w", err)
	}
	stock, err := s.client.Get(ctx, key).Int64()
	if err != nil {
		return 0, fmt.Errorf("read stock cache after seed: %w", err)
	}
	return stock, nil
}

func (s *Store) Reserve(ctx context.Context, productID string, quantity int64) (int64, error) {
	result, err := reserveScript.Run(ctx, s.client, []string{stockKey(productID)}, quantity).Int64()
	if err != nil {
		return 0, fmt.Errorf("reserve inventory: %w", err)
	}
	switch result {
	case -1:
		return 0, ErrStockMissing
	case -2:
		return 0, ErrSoldOut
	case -3:
		return 0, errors.New("quantity must be positive")
	default:
		return result, nil
	}
}

func (s *Store) Release(ctx context.Context, productID string, quantity int64) error {
	if err := s.client.IncrBy(ctx, stockKey(productID), quantity).Err(); err != nil {
		return fmt.Errorf("release inventory reservation: %w", err)
	}
	return nil
}

func (s *Store) Publish(ctx context.Context, showID string, event model.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if err := s.client.Publish(ctx, eventChannel(showID), payload).Err(); err != nil {
		return fmt.Errorf("publish show event: %w", err)
	}
	return nil
}

func (s *Store) Subscribe(ctx context.Context) (*redis.PubSub, error) {
	pubsub := s.client.PSubscribe(ctx, "show:*:events")
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, fmt.Errorf("subscribe to show events: %w", err)
	}
	return pubsub, nil
}

func (s *Store) AllowRequest(ctx context.Context, subject string, requestsPerSecond int) (bool, error) {
	if requestsPerSecond <= 0 {
		return false, errors.New("rate limit must be positive")
	}
	result, err := rateLimitScript.Run(ctx, s.client, []string{"rate-limit:" + subject},
		strconv.Itoa(requestsPerSecond), strconv.Itoa(requestsPerSecond*2)).Int64()
	if err != nil {
		return false, fmt.Errorf("check request rate limit: %w", err)
	}
	return result == 1, nil
}

func (s *Store) CountViewers(ctx context.Context, showID string, delta int64) (int64, error) {
	key := "show:" + showID + ":viewers"
	count, err := viewerCountScript.Run(ctx, s.client, []string{key}, delta).Int64()
	if err != nil {
		return 0, fmt.Errorf("update viewer count: %w", err)
	}
	return count, nil
}

func (s *Store) ViewerCount(ctx context.Context, showID string) (int64, error) {
	count, err := s.client.Get(ctx, "show:"+showID+":viewers").Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read viewer count: %w", err)
	}
	return count, nil
}

func stockKey(productID string) string  { return "product:" + productID + ":stock" }
func eventChannel(showID string) string { return "show:" + showID + ":events" }
