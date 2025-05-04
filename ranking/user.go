package ranking

import (
	"encoding/json"
	"log"
	"sort"
)

// GetTop5 возвращает топ-5 пользователей по рейтингу.
func (r *Ranking) GetTop5() []User {
	keys, err := r.redis.Keys(r.ctx, "user:*").Result()
	if err != nil {
		log.Printf("Не удалось получить ключи пользователей из Redis: %v", err)
		return nil
	}

	users := make([]User, 0)
	for _, key := range keys {
		data, err := r.redis.Get(r.ctx, key).Result()
		if err != nil {
			log.Printf("Не удалось загрузить данные пользователя %s из Redis: %v", key, err)
			continue
		}
		var user User
		if err := json.Unmarshal([]byte(data), &user); err != nil {
			log.Printf("Не удалось разобрать данные пользователя %s: %v", key, err)
			continue
		}
		if user.Rating > 0 {
			users = append(users, user)
		}
	}

	sort.Slice(users, func(i, j int) bool {
		return users[i].Rating > users[j].Rating
	})

	if len(users) > 5 {
		return users[:5]
	}
	log.Printf("Топ-5 пользователей: %v", users)
	return users
}
