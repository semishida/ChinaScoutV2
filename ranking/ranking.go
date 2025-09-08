package ranking

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/go-redis/redis/v8"
	"github.com/joho/godotenv"
)

// Ranking управляет рейтингами, опросами, играми и голосовой активностью.
type Ranking struct {
	mu                sync.Mutex
	admins            map[string]bool
	polls             map[string]*Poll
	duels             map[string]*Duel
	redis             *redis.Client
	ctx               context.Context
	voiceAct          map[string]int
	redBlackGames     map[string]*RedBlackGame
	blackjackGames    map[string]*BlackjackGame
	floodChannelID    string
	logChannelID      string
	cinemaOptions     []CinemaOption
	pendingCinemaBids map[string]PendingCinemaBid
	cinemaChannelID   string
}

// NewRanking инициализирует структуру Ranking.
func NewRanking(adminFilePath, redisAddr, floodChannelID, cinemaChannelID string) (*Ranking, error) {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Ошибка загрузки .env файла")
	}

	r := &Ranking{
		admins:            make(map[string]bool),
		polls:             make(map[string]*Poll),
		duels:             make(map[string]*Duel),
		voiceAct:          map[string]int{},
		redBlackGames:     make(map[string]*RedBlackGame),
		blackjackGames:    make(map[string]*BlackjackGame),
		ctx:               context.Background(),
		floodChannelID:    floodChannelID,
		logChannelID:      os.Getenv("LOG_CHANNEL_ID"),
		cinemaOptions:     []CinemaOption{},
		pendingCinemaBids: make(map[string]PendingCinemaBid),
		cinemaChannelID:   cinemaChannelID,
	}

	// Подключение к Redis с повторными попытками
	var redisErr error
	for i := 0; i < 5; i++ {
		r.redis = redis.NewClient(&redis.Options{
			Addr:     redisAddr,
			Password: os.Getenv("REDIS_PASSWORD"),
		})
		_, redisErr = r.redis.Ping(r.ctx).Result()
		if redisErr == nil {
			break
		}
		log.Printf("Не удалось подключиться к Redis (попытка %d/5): %v", i+1, redisErr)
		time.Sleep(5 * time.Second)
	}
	if redisErr != nil {
		return nil, fmt.Errorf("не удалось подключиться к Redis после 5 попыток: %v", redisErr)
	}

	// Загрузка администраторов из файла
	file, err := os.Open(adminFilePath)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть файл администраторов: %v", err)
	}
	defer file.Close()

	var admins struct {
		IDs []string `json:"admin_ids"`
	}
	if err := json.NewDecoder(file).Decode(&admins); err != nil {
		return nil, fmt.Errorf("не удалось разобрать файл администраторов: %v", err)
	}
	for _, id := range admins.IDs {
		r.admins[id] = true
	}

	// Загрузка cinema options
	r.LoadCinemaOptions()

	log.Printf("Инициализирован рейтинг с %d администраторами", len(r.admins))
	return r, nil
}

// IsAdmin проверяет, является ли пользователь администратором.
func (r *Ranking) IsAdmin(userID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	isAdmin := r.admins[userID]
	log.Printf("Проверка администратора %s: %v", userID, isAdmin)
	return isAdmin
}

// generateGameID создаёт уникальный ID для игры.
func generateGameID(playerID string) string {
	rand.Seed(time.Now().UnixNano())
	// Заменяем _ на - для избежания проблем с парсингом
	safePlayerID := strings.ReplaceAll(playerID, "_", "-")
	return fmt.Sprintf("%s-%d-%d", safePlayerID, time.Now().UnixNano(), rand.Intn(10000))
}

// randomColor возвращает случайный цвет в формате RGB.
func randomColor() int {
	return rand.Intn(0xFFFFFF)
}

// generatePollID создаёт уникальный 5-символьный ID для опроса.
func generatePollID() string {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	rand.Seed(time.Now().UnixNano())
	id := make([]byte, 5)
	for i := range id {
		id[i] = letters[rand.Intn(len(letters))]
	}
	return string(id)
}

// LogCreditOperation отправляет лог операции с кредитами в канал логов.
func (r *Ranking) LogCreditOperation(s *discordgo.Session, message string) {
	if r.logChannelID != "" {
		_, err := s.ChannelMessageSend(r.logChannelID, message)
		if err != nil {
			log.Printf("Не удалось отправить лог в канал %s: %v", r.logChannelID, err)
		}
	}
}
