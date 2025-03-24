package ranking

import (
	"context"
	"encoding/json"
	"github.com/redis/go-redis/v9"
)

type RedisClient struct {
	client *redis.Client
	ctx    context.Context
}

func NewRedisClient(addr string) (*RedisClient, error) {
	client := redis.NewClient(&redis.Options{
		Addr: addr,
	})
	ctx := context.Background()

	_, err := client.Ping(ctx).Result()
	if err != nil {
		return nil, err
	}

	return &RedisClient{client: client, ctx: ctx}, nil
}

func (r *RedisClient) SaveUser(user *User) error {
	data, err := json.Marshal(user)
	if err != nil {
		return err
	}
	return r.client.Set(r.ctx, user.ID, data, 0).Err()
}

func (r *RedisClient) LoadUser(id string) (*User, error) {
	data, err := r.client.Get(r.ctx, id).Bytes()
	if err == redis.Nil {
		return &User{ID: id, Rating: 0}, nil
	} else if err != nil {
		return nil, err
	}

	var user User
	if err := json.Unmarshal(data, &user); err != nil {
		return nil, err
	}
	return &user, nil
}
