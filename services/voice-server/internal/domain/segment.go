package domain

// Segment — фрагмент транскрипта из ответа voice-worker (JSON).
type Segment struct {
	Speaker    string   `json:"speaker"`
	Text       string   `json:"text"`
	AbsStart   float64  `json:"abs_start"`
	AbsEnd     float64  `json:"abs_end"`
	VoiceID    string   `json:"voice_id,omitempty"`
	MatchScore *float64 `json:"match_score,omitempty"`
}
