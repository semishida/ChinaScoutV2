package ranking

import (
	"log"
	"time"

	"github.com/bwmarrin/discordgo"
)

// TrackVoiceActivity отслеживает голосовую активность и начисляет кредиты.
func (r *Ranking) TrackVoiceActivity(s *discordgo.Session, vs *discordgo.VoiceStateUpdate) {
	userID := vs.UserID
	channelID := vs.ChannelID
	log.Printf("TrackVoiceActivity вызван для пользователя %s, канал: %s", userID, channelID)

	if channelID == "" {
		r.mu.Lock()
		if seconds, exists := r.voiceAct[userID]; exists {
			r.UpdateVoiceSeconds(userID, seconds)
			log.Printf("Пользователь %s покинул голосовой канал, сохранено %d секунд", userID, seconds)
		}
		delete(r.voiceAct, userID)
		r.mu.Unlock()
		log.Printf("Пользователь %s покинул голосовой канал, голосовая активность сброшена", userID)
		return
	}

	r.mu.Lock()
	if _, exists := r.voiceAct[userID]; !exists {
		r.voiceAct[userID] = 0
		go r.startVoiceTracking(s, userID)
		log.Printf("Начато отслеживание голосовой активности для %s", userID)
	}
	r.mu.Unlock()
}

// startVoiceTracking запускает цикл отслеживания голосовой активности.
func (r *Ranking) startVoiceTracking(s *discordgo.Session, userID string) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.mu.Lock()
			if seconds, exists := r.voiceAct[userID]; exists {
				r.voiceAct[userID] = seconds + 1
				r.UpdateVoiceSeconds(userID, 1) // Обновляем VoiceSeconds в Redis
				if r.voiceAct[userID]%60 == 0 { // Начисляем 1 поинт каждые 60 секунд
					r.UpdateRating(userID, 1)
					log.Printf("Начислен 1 соцкредит пользователю %s за %d секунд голосовой активности", userID, r.voiceAct[userID])
				}
				//log.Printf("Обновлено время для %s: %d секунд", userID, r.voiceAct[userID])
			} else {
				r.mu.Unlock()
				log.Printf("Остановлено отслеживание для %s: пользователь не в голосовом канале", userID)
				return
			}
			r.mu.Unlock()
		}
	}
}
