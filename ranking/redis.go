package ranking

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/go-redis/redis/v8"
)

// User представляет пользователя и его рейтинг.
type User struct {
	ID           string `json:"id"`
	Rating       int    `json:"rating"`
	DuelsPlayed  int    `json:"duels_played"`
	DuelsWon     int    `json:"duels_won"`
	RBPlayed     int    `json:"rb_played"`
	RBWon        int    `json:"rb_won"`
	BJPlayed     int    `json:"bj_played"`
	BJWon        int    `json:"bj_won"`
	VoiceSeconds int    `json:"voice_seconds"`
}

// GetRating получает рейтинг пользователя из Redis.
func (r *Ranking) GetRating(userID string) int {
	for i := 0; i < 3; i++ {
		data, err := r.redis.Get(r.ctx, "user:"+userID).Result()
		if err == redis.Nil {
			return 0
		}
		if err != nil {
			log.Printf("Не удалось получить рейтинг для %s из Redis (попытка %d/3): %v", userID, i+1, err)
			time.Sleep(1 * time.Second)
			continue
		}
		var user User
		if err := json.Unmarshal([]byte(data), &user); err != nil {
			log.Printf("Не удалось разобрать данные пользователя %s: %v", userID, err)
			return 0
		}
		return user.Rating
	}
	log.Printf("Не удалось получить рейтинг для %s после 3 попыток", userID)
	return 0
}

// UpdateRating обновляет рейтинг пользователя в Redis.
func (r *Ranking) UpdateRating(userID string, points int) {
	user := User{ID: userID}
	for i := 0; i < 3; i++ {
		data, err := r.redis.Get(r.ctx, "user:"+userID).Result()
		if err == nil {
			if err := json.Unmarshal([]byte(data), &user); err != nil {
				log.Printf("Не удалось разобрать данные пользователя %s: %v", userID, err)
				return
			}
			break
		} else if err == redis.Nil {
			break
		} else {
			log.Printf("Не удалось получить данные пользователя %s из Redis (попытка %d/3): %v", userID, i+1, err)
			time.Sleep(1 * time.Second)
		}
	}

	oldRating := user.Rating
	user.Rating += points
	if user.Rating < 0 {
		user.Rating = 0
	}

	dataBytes, err := json.Marshal(user)
	if err != nil {
		log.Printf("Не удалось сериализовать данные пользователя %s: %v", userID, err)
		return
	}

	for i := 0; i < 3; i++ {
		if err := r.redis.Set(r.ctx, "user:"+userID, dataBytes, 0).Err(); err != nil {
			log.Printf("Не удалось сохранить данные пользователя %s в Redis (попытка %d/3): %v", userID, i+1, err)
			time.Sleep(1 * time.Second)
			continue
		}
		log.Printf("Обновлён рейтинг для %s: %d (изменение: %d)", userID, user.Rating, points)
		// Логируем операцию в LOG_CHANNEL_ID
		s, err := discordgo.New("Bot " + os.Getenv("DISCORD_TOKEN"))
		if err == nil {
			if points == 1 { // Предполагаем, что +1 — это за голосовую активность
				r.LogCreditOperation(s, fmt.Sprintf("<@%s> получил +1 кредит за активность в войсе %d -> %d", userID, oldRating, user.Rating))
			} else {
				r.LogCreditOperation(s, fmt.Sprintf("💰 <@%s> изменил баланс: %d → %d (%+d кредитов)", userID, oldRating, user.Rating, points))
			}
		}
		return
	}
	log.Printf("Не удалось сохранить данные пользователя %s в Redis после 3 попыток", userID)
	if r.floodChannelID != "" {
		s, err := discordgo.New("Bot " + os.Getenv("DISCORD_TOKEN"))
		if err == nil {
			s.ChannelMessageSend(r.floodChannelID, "❌ Ошибка: Не удалось сохранить рейтинг в Redis после 3 попыток! Проверьте Redis-сервер.")
		}
	}
}

// UpdateDuelStats обновляет статистику дуэлей пользователя.
func (r *Ranking) UpdateDuelStats(userID string, won bool) {
	user := User{ID: userID}
	for i := 0; i < 3; i++ {
		data, err := r.redis.Get(r.ctx, "user:"+userID).Result()
		if err == nil {
			if err := json.Unmarshal([]byte(data), &user); err != nil {
				log.Printf("Не удалось разобрать данные пользователя %s: %v", userID, err)
				return
			}
			break
		} else if err == redis.Nil {
			break
		} else {
			log.Printf("Не удалось получить данные пользователя %s из Redis (попытка %d/3): %v", userID, i+1, err)
			time.Sleep(1 * time.Second)
		}
	}

	user.DuelsPlayed++
	if won {
		user.DuelsWon++
	}

	dataBytes, err := json.Marshal(user)
	if err != nil {
		log.Printf("Не удалось сериализовать данные пользователя %s: %v", userID, err)
		return
	}

	for i := 0; i < 3; i++ {
		if err := r.redis.Set(r.ctx, "user:"+userID, dataBytes, 0).Err(); err != nil {
			log.Printf("Не удалось сохранить данные пользователя %s в Redis (попытка %d/3): %v", userID, i+1, err)
			time.Sleep(1 * time.Second)
			continue
		}
		log.Printf("Обновлена статистика дуэлей для %s: сыграно %d, выиграно %d", userID, user.DuelsPlayed, user.DuelsWon)
		return
	}
	log.Printf("Не удалось сохранить данные пользователя %s в Redis после 3 попыток", userID)
}

// UpdateRBStats обновляет статистику RedBlack.
func (r *Ranking) UpdateRBStats(userID string, won bool) {
	user := User{ID: userID}
	for i := 0; i < 3; i++ {
		data, err := r.redis.Get(r.ctx, "user:"+userID).Result()
		if err == nil {
			if err := json.Unmarshal([]byte(data), &user); err != nil {
				log.Printf("Не удалось разобрать данные пользователя %s: %v", userID, err)
				return
			}
			break
		} else if err == redis.Nil {
			break
		} else {
			log.Printf("Не удалось получить данные пользователя %s из Redis (попытка %d/3): %v", userID, i+1, err)
			time.Sleep(1 * time.Second)
		}
	}

	user.RBPlayed++
	if won {
		user.RBWon++
	}

	dataBytes, err := json.Marshal(user)
	if err != nil {
		log.Printf("Не удалось сериализовать данные пользователя %s: %v", userID, err)
		return
	}

	for i := 0; i < 3; i++ {
		if err := r.redis.Set(r.ctx, "user:"+userID, dataBytes, 0).Err(); err != nil {
			log.Printf("Не удалось сохранить данные пользователя %s в Redis (попытка %d/3): %v", userID, i+1, err)
			time.Sleep(1 * time.Second)
			continue
		}
		log.Printf("Обновлена статистика RedBlack для %s: сыграно %d, выиграно %d", userID, user.RBPlayed, user.RBWon)
		return
	}
	log.Printf("Не удалось сохранить данные пользователя %s в Redis после 3 попыток", userID)
}

// UpdateBJStats обновляет статистику Blackjack.
func (r *Ranking) UpdateBJStats(userID string, won bool) {
	user := User{ID: userID}
	for i := 0; i < 3; i++ {
		data, err := r.redis.Get(r.ctx, "user:"+userID).Result()
		if err == nil {
			if err := json.Unmarshal([]byte(data), &user); err != nil {
				log.Printf("Не удалось разобрать данные пользователя %s: %v", userID, err)
				return
			}
			break
		} else if err == redis.Nil {
			break
		} else {
			log.Printf("Не удалось получить данные пользователя %s из Redis (попытка %d/3): %v", userID, i+1, err)
			time.Sleep(1 * time.Second)
		}
	}

	user.BJPlayed++
	if won {
		user.BJWon++
	}

	dataBytes, err := json.Marshal(user)
	if err != nil {
		log.Printf("Не удалось сериализовать данные пользователя %s: %v", userID, err)
		return
	}

	for i := 0; i < 3; i++ {
		if err := r.redis.Set(r.ctx, "user:"+userID, dataBytes, 0).Err(); err != nil {
			log.Printf("Не удалось сохранить данные пользователя %s в Redis (попытка %d/3): %v", userID, i+1, err)
			time.Sleep(1 * time.Second)
			continue
		}
		log.Printf("Обновлена статистика Blackjack для %s: сыграно %d, выиграно %d", userID, user.BJPlayed, user.BJWon)
		return
	}
	log.Printf("Не удалось сохранить данные пользователя %s в Redis после 3 попыток", userID)
}

// UpdateVoiceSeconds обновляет время в голосовых каналах (в секундах).
func (r *Ranking) UpdateVoiceSeconds(userID string, seconds int) {
	user := User{ID: userID}
	for i := 0; i < 3; i++ {
		data, err := r.redis.Get(r.ctx, "user:"+userID).Result()
		if err == nil {
			if err := json.Unmarshal([]byte(data), &user); err != nil {
				log.Printf("Не удалось разобрать данные пользователя %s: %v", userID, err)
				return
			}
			break
		} else if err == redis.Nil {
			break
		} else {
			log.Printf("Не удалось получить данные пользователя %s из Redis (попытка %d/3): %v", userID, i+1, err)
			time.Sleep(1 * time.Second)
		}
	}

	user.VoiceSeconds += seconds

	dataBytes, err := json.Marshal(user)
	if err != nil {
		log.Printf("Не удалось сериализовать данные пользователя %s: %v", userID, err)
		return
	}

	for i := 0; i < 3; i++ {
		if err := r.redis.Set(r.ctx, "user:"+userID, dataBytes, 0).Err(); err != nil {
			log.Printf("Не удалось сохранить данные пользователя %s в Redis (попытка %d/3): %v", userID, i+1, err)
			time.Sleep(1 * time.Second)
			continue
		}
		//log.Printf("Обновлено время в голосовых каналах для %s: %d секунд", userID)
		return
	}
	log.Printf("Не удалось сохранить данные пользователя %s в Redis после 3 попыток", userID)
}
