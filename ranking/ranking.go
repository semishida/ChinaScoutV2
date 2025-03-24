package ranking

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type User struct {
	ID     string `json:"id"`
	Rating int    `json:"rating"`
}

type Ranking struct {
	mu     sync.Mutex
	users  map[string]*User
	admins map[string]bool
	redis  *RedisClient
}

func NewRanking(adminFilePath, redisAddr string) (*Ranking, error) {
	r := &Ranking{
		users:  make(map[string]*User),
		admins: make(map[string]bool),
	}

	redisClient, err := NewRedisClient(redisAddr)
	if err != nil {
		return nil, err
	}
	r.redis = redisClient

	file, err := os.Open(adminFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open admin file: %v", err)
	}
	defer file.Close()

	var admins struct {
		IDs []string `json:"admin_ids"`
	}
	if err := json.NewDecoder(file).Decode(&admins); err != nil {
		return nil, fmt.Errorf("failed to parse admin file: %v", err)
	}

	for _, id := range admins.IDs {
		r.admins[id] = true
	}

	if err := r.LoadAllFromRedis(); err != nil {
		log.Printf("Failed to load users from Redis: %v", err)
	}

	log.Printf("Initialized ranking with %d admins and %d users from Redis", len(r.admins), len(r.users))
	return r, nil
}

func (r *Ranking) LoadAllFromRedis() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	keys, err := r.redis.client.Keys(r.redis.ctx, "*").Result()
	if err != nil {
		return fmt.Errorf("failed to get keys from Redis: %v", err)
	}

	for _, key := range keys {
		user, err := r.redis.LoadUser(key)
		if err != nil {
			log.Printf("Failed to load user %s from Redis: %v", key, err)
			continue
		}
		r.users[user.ID] = user
	}

	return nil
}

func (r *Ranking) PeriodicSave() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		r.mu.Lock()
		for _, user := range r.users {
			if err := r.redis.SaveUser(user); err != nil {
				log.Printf("Failed to save user to Redis: %v", err)
			}
		}
		r.mu.Unlock()
	}
}

func (r *Ranking) AddUser(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.users[id]; !exists {
		r.users[id] = &User{ID: id, Rating: 0}
		r.redis.SaveUser(r.users[id])
	}
}

func (r *Ranking) UpdateRating(id string, points int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	user, err := r.redis.LoadUser(id)
	if err != nil {
		user = &User{ID: id, Rating: 0}
	}
	user.Rating += points
	r.users[id] = user
	r.redis.SaveUser(user)
}

func (r *Ranking) GetRating(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	if user, exists := r.users[id]; exists {
		return user.Rating
	}
	user, err := r.redis.LoadUser(id)
	if err != nil {
		return 0
	}
	r.users[id] = user
	return user.Rating
}

func (r *Ranking) GetTop5() []User {
	r.mu.Lock()
	defer r.mu.Unlock()

	users := make([]User, 0, len(r.users))
	for _, user := range r.users {
		users = append(users, *user)
	}

	sort.Slice(users, func(i, j int) bool {
		return users[i].Rating > users[j].Rating
	})

	if len(users) > 5 {
		return users[:5]
	}
	return users
}

func (r *Ranking) IsAdmin(userID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, isAdmin := r.admins[userID]
	return isAdmin
}

func (r *Ranking) HandleChinaCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Checking if user %s is an admin", m.Author.ID)

	userID := strings.TrimPrefix(m.Author.ID, "<@")
	userID = strings.TrimSuffix(userID, ">")
	userID = strings.TrimPrefix(userID, "!")

	if !r.IsAdmin(userID) {
		s.ChannelMessageSend(m.ChannelID, "❌ Глупый Китайский мальчик хочет использовать привелегии Китай-Партии.")
		return
	}

	parts := strings.Fields(command)
	if len(parts) < 3 {
		s.ChannelMessageSend(m.ChannelID, "❌ Глупый Китайский брат. Используй привелегии правильно: !china @id +10 или !china @id -10.")
		return
	}

	targetID := strings.TrimPrefix(parts[1], "<@")
	targetID = strings.TrimSuffix(targetID, ">")
	targetID = strings.TrimPrefix(targetID, "!")

	points, err := strconv.Atoi(parts[2])
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ Глупый количество очков. Используй целое число.")
		return
	}

	// Извлекаем причину (всё, что после очков)
	reason := ""
	if len(parts) > 3 {
		reason = strings.Join(parts[3:], " ")
	}

	r.UpdateRating(targetID, points)

	// Формируем сообщение с причиной, если она есть
	response := fmt.Sprintf("✅ Социальные кредиты пользователя <@%s> изменились на %d баллов.", targetID, points)
	if reason != "" {
		response += fmt.Sprintf(" По причине: %s", reason)
	}
	s.ChannelMessageSend(m.ChannelID, response)
}

func (r *Ranking) TrackVoiceActivity(s *discordgo.Session) {
	s.AddHandler(func(s *discordgo.Session, v *discordgo.VoiceStateUpdate) {
		if v.UserID == s.State.User.ID {
			return
		}

		if v.ChannelID != "" {
			log.Printf("User %s joined voice channel %s", v.UserID, v.ChannelID)
			r.AddUser(v.UserID)
			go r.trackUser(s, v.UserID, v.ChannelID)
		}
	})
}

func (r *Ranking) trackUser(s *discordgo.Session, userID string, channelID string) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			inChannel := false

			guilds, err := s.UserGuilds(100, "", "", false)
			if err != nil {
				log.Printf("Failed to get user guilds: %v", err)
				return
			}

			for _, guild := range guilds {
				guildState, err := s.State.Guild(guild.ID)
				if err != nil {
					log.Printf("Failed to get guild state for guild %s: %v", guild.ID, err)
					continue
				}

				for _, vs := range guildState.VoiceStates {
					if vs.UserID == userID && vs.ChannelID == channelID {
						inChannel = true
						break
					}
				}
				if inChannel {
					break
				}
			}

			if inChannel {
				r.UpdateRating(userID, 1)
				log.Printf("User %s has been in voice channel %s for 1 minute. Rating increased by 1.", userID, channelID)
			} else {
				log.Printf("User %s left voice channel %s", userID, channelID)
				return
			}
		}
	}
}
