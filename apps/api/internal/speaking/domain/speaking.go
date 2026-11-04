package domain

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

var ErrNotFound = errors.New("speaking session not found")
var ErrConflict = errors.New("this interview changed; reload it to continue")
var ErrUnavailable = errors.New("speaking is temporarily unavailable; try again shortly")
var ErrLimit = errors.New("daily speaking limit reached: three interviews and five report requests per UTC day")
var ErrInvalid = errors.New("record a clear answer before continuing, or choose Skip answer")
var ErrConsent = errors.New("consent to process recorded answers is required before starting")

const Rubric = "speaking-v1"
const MaxAudio = 4_200_000

type Examiner struct {
	Country string `json:"country"`
	Code    string `json:"code"`
	Gender  string `json:"gender"`
	Accent  string `json:"accent"`
	Voice   string `json:"voice"`
}
type Country struct {
	Name   string     `json:"name"`
	Code   string     `json:"code"`
	Voices []Examiner `json:"-"`
}

// Catalog IDs and metadata verified against Gemini Voices API. Country is a
// voice region, not a claim that every speaker in that country shares an accent.
var Countries = []Country{
	{"Australia", "AU", []Examiner{{"Australia", "AU", "female", "Sydney English", "en-au-advisor-5"}, {"Australia", "AU", "male", "Sydney English", "en-au-advisor-1"}}},
	{"Canada", "CA", []Examiner{{"Canada", "CA", "female", "Vancouver English", "en-ca-advisor-2"}, {"Canada", "CA", "male", "Toronto English", "en-ca-advisor-1"}}},
	{"United Kingdom", "GB", []Examiner{{"United Kingdom", "GB", "female", "Winchester English", "en-gb-advisor-1"}, {"United Kingdom", "GB", "male", "Bristol English", "en-gb-advisor-10"}}},
	{"United States", "US", []Examiner{{"United States", "US", "female", "General American", "achernar"}, {"United States", "US", "male", "General American", "achird"}}},
	{"Ireland", "IE", []Examiner{{"Ireland", "IE", "female", "Dublin English", "en-ie-advisor-2"}, {"Ireland", "IE", "male", "Dublin English", "en-ie-advisor-1"}}},
	{"New Zealand", "NZ", []Examiner{{"New Zealand", "NZ", "female", "Auckland New Zealand English", "en-nz-advisor-2"}, {"New Zealand", "NZ", "male", "Auckland New Zealand English", "en-nz-advisor-4"}}},
}

func RandomExaminer() (Examiner, error) {
	c, e := rand.Int(rand.Reader, big.NewInt(int64(len(Countries))))
	if e != nil {
		return Examiner{}, e
	}
	g, e := rand.Int(rand.Reader, big.NewInt(2))
	if e != nil {
		return Examiner{}, e
	}
	return Countries[c.Int64()].Voices[g.Int64()], nil
}

type Turn struct {
	Transcript    string `json:"-"`
	Position      int32  `json:"position"`
	Part          int32  `json:"part"`
	Prompt        string `json:"prompt"`
	Seconds       int32  `json:"seconds"`
	PrepSeconds   int32  `json:"prep_seconds"`
	VoiceStatus   string `json:"voice_status"`
	VoiceAttempts int32  `json:"voice_attempts"`
	DurationMS    int32  `json:"duration_ms"`
	Answered      bool   `json:"answered"`
	Skipped       bool   `json:"skipped"`
	HasRecording  bool   `json:"has_recording"`
	Recording     []byte `json:"-"`
}
type Session struct {
	Evaluation     *EvaluationProgress `json:"evaluation,omitempty"`
	Mode           string              `json:"mode"`
	LiveModel      string              `json:"-"`
	LiveRunID      string              `json:"-"`
	ID             string              `json:"id"`
	UserID         string              `json:"-"`
	RunID          string              `json:"-"`
	Examiner       Examiner            `json:"examiner"`
	Topic          string              `json:"topic"`
	Status         string              `json:"status"`
	Model          string              `json:"-"`
	RubricVersion  string              `json:"rubric_version"`
	ErrorCode      string              `json:"error_code"`
	Report         json.RawMessage     `json:"report,omitempty"`
	Turns          []Turn              `json:"turns"`
	CreatedAt      time.Time           `json:"created_at"`
	AudioExpiresAt time.Time           `json:"audio_expires_at"`
}
type VoiceJob struct {
	SessionID string
	Position  int32
	RunID     string
	Prompt    string
	Examiner  Examiner
}

type PlanData struct {
	Turns []Turn `json:"turns"`
}

func ParsePlan(title string, raw []byte) (string, []Turn, error) {
	var v PlanData
	if json.Unmarshal(raw, &v) != nil || len(v.Turns) != 11 {
		return "", nil, ErrUnavailable
	}
	for i := range v.Turns {
		v.Turns[i].Position = int32(i)
		if strings.TrimSpace(v.Turns[i].Prompt) == "" || v.Turns[i].Part < 1 || v.Turns[i].Part > 3 || v.Turns[i].Seconds < 20 || v.Turns[i].Seconds > 120 {
			return "", nil, ErrUnavailable
		}
	}
	return title, v.Turns, nil
}

func NewPlan() (string, []Turn, error) {
	n, e := rand.Int(rand.Reader, big.NewInt(2))
	if e != nil {
		return "", nil, e
	}
	title := "Learning something new"
	prompts := []string{
		"Welcome to your speaking practice. We will begin with some questions about everyday life. Do you work or study, and what do you enjoy about it?",
		"What is a typical weekday like for you?", "What do you like about the area where you live?",
		"Now let us talk about learning. What is something you enjoyed learning as a child?", "Do you prefer learning alone or with other people? Why?", "How do you usually find time to learn something new?",
		"Now we come to Part 2. Describe a useful skill you learned. Say what the skill is, when and how you learned it, and who helped you. Explain why this skill has been useful. You will have one minute to prepare, then up to two minutes to speak.",
		"We have been discussing learning a skill. In Part 3, let us consider learning more broadly. Why do some adults find it difficult to learn new skills?",
		"How has technology changed the way people learn?", "Should schools give more time to practical skills or academic knowledge? Why?", "Who should be responsible for helping workers learn new skills: employers, governments, or individuals?",
	}
	if n.Int64() == 1 {
		title = "A place worth visiting"
		prompts[3] = "Now let us talk about places. Where do you like to spend your free time?"
		prompts[4] = "Do you prefer visiting familiar places or discovering new ones? Why?"
		prompts[5] = "Has the place where you live changed since you were younger?"
		prompts[6] = "Now we come to Part 2. Describe a place you visited that you would recommend to a friend. Say where it is, when you visited, and what you did there. Explain why your friend would enjoy it. You will have one minute to prepare, then up to two minutes to speak."
		prompts[7] = "We have been discussing a place you visited. In Part 3, let us consider tourism more broadly. What makes a place attractive to visitors?"
		prompts[8] = "How can tourism benefit the people who live in a place?"
		prompts[9] = "What problems can large numbers of visitors cause, and how could these be reduced?"
		prompts[10] = "Do you think virtual experiences will ever replace travelling to a place in person? Why or why not?"
	}
	turns := make([]Turn, len(prompts))
	for i, p := range prompts {
		part, seconds, prep := int32(1), int32(40), int32(0)
		if i == 6 {
			part, seconds, prep = 2, 120, 60
		}
		if i > 6 {
			part, seconds = 3, 60
		}
		turns[i] = Turn{Position: int32(i), Part: part, Seconds: seconds, PrepSeconds: prep, Prompt: p}
	}
	return title, turns, nil
}

// FocusPlan keeps the complete interview for part 0, or creates a focused
// practice session for one IELTS Speaking part. Positions are reindexed because
// answer progression is deliberately sequential inside a session.
func FocusPlan(topic string, turns []Turn, part int32) (string, []Turn, error) {
	if part == 0 {
		return topic, turns, nil
	}
	if part < 1 || part > 3 {
		return "", nil, ErrInvalid
	}
	focused := make([]Turn, 0, len(turns))
	for _, turn := range turns {
		if turn.Part != part {
			continue
		}
		turn.Position = int32(len(focused))
		focused = append(focused, turn)
	}
	if len(focused) == 0 {
		return "", nil, ErrInvalid
	}
	return fmt.Sprintf("%s · Part %d", topic, part), focused, nil
}

// Validate canonical 16 kHz mono PCM16 WAV and derive duration from bytes, never
// a client-supplied duration. Browser recordings are normalized before upload.
func ValidateAudio(b []byte, seconds int32) (int32, error) {
	if len(b) < 44 || len(b) > MaxAudio || string(b[:4]) != "RIFF" || string(b[8:16]) != "WAVEfmt " || binary.LittleEndian.Uint32(b[16:20]) != 16 || binary.LittleEndian.Uint16(b[20:22]) != 1 || binary.LittleEndian.Uint16(b[22:24]) != 1 || binary.LittleEndian.Uint32(b[24:28]) != 16000 || binary.LittleEndian.Uint32(b[28:32]) != 32000 || binary.LittleEndian.Uint16(b[32:34]) != 2 || binary.LittleEndian.Uint16(b[34:36]) != 16 || string(b[36:40]) != "data" || int(binary.LittleEndian.Uint32(b[40:44])) != len(b)-44 || int(binary.LittleEndian.Uint32(b[4:8])) != len(b)-8 || (len(b)-44)%2 != 0 {
		return 0, ErrInvalid
	}
	duration := int32((len(b) - 44) * 1000 / 32000)
	if duration < 1000 || duration > (seconds+2)*1000 {
		return 0, ErrInvalid
	}
	energy := 0.0
	for i := 44; i < len(b); i += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(b[i:i+2]))) / 32768
		energy += v * v
	}
	if math.Sqrt(energy/float64((len(b)-44)/2)) < 0.001 {
		return 0, ErrInvalid
	}
	return duration, nil
}

type Criterion struct {
	Key     string   `json:"key"`
	Score   *float64 `json:"score"`
	Reason  string   `json:"reason"`
	Improve string   `json:"how_to_improve"`
}
type Response struct {
	Position   int32  `json:"position"`
	Transcript string `json:"transcript"`
	Feedback   string `json:"feedback"`
}
type Issue struct {
	Category    string  `json:"category"`
	Position    int32   `json:"position"`
	Second      float64 `json:"second"`
	Quote       string  `json:"quote"`
	Explanation string  `json:"explanation"`
	Suggestion  string  `json:"suggestion"`
}
type Report struct {
	Overall      *float64    `json:"overall"`
	Assessable   bool        `json:"assessable"`
	Summary      string      `json:"summary"`
	ModelVersion string      `json:"model_version"`
	Criteria     []Criterion `json:"criteria"`
	Responses    []Response  `json:"responses"`
	Issues       []Issue     `json:"issues"`
	Strengths    []string    `json:"strengths"`
	NextSteps    []string    `json:"next_steps"`
}

func ParseReport(raw []byte, s Session) (Report, error) {
	var r Report
	if len(raw) > 90000 {
		return r, invalidReport("payload_too_large")
	}
	if json.Unmarshal(raw, &r) != nil {
		return r, invalidReport("malformed_json")
	}
	if strings.TrimSpace(r.Summary) == "" {
		return r, invalidReport("missing_summary")
	}
	if len(r.Criteria) != 4 {
		return r, invalidReport("criteria_count")
	}
	if len(r.Responses) != len(s.Turns) {
		return r, invalidReport("response_count")
	}
	if len(r.Issues) > 15 {
		return r, invalidReport("issues_count")
	}
	if len(r.NextSteps) < 1 || len(r.NextSteps) > 5 {
		return r, invalidReport("next_steps_count")
	}
	if len(r.Strengths) > 5 {
		return r, invalidReport("strengths_count")
	}
	allowed := map[string]bool{"fluency_coherence": true, "lexical_resource": true, "grammar": true, "pronunciation": true}
	seen := map[string]bool{}
	sum := 0.0
	for i := range r.Criteria {
		c := &r.Criteria[i]
		if !allowed[c.Key] || seen[c.Key] || strings.TrimSpace(c.Reason) == "" || strings.TrimSpace(c.Improve) == "" {
			return r, invalidReport("criterion_fields")
		}
		seen[c.Key] = true
		if c.Score != nil {
			v := *c.Score
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 9 {
				return r, invalidReport("criterion_score_range")
			}
			v = math.Round(v*2) / 2
			c.Score = &v
			sum += v
		} else if r.Assessable {
			return r, invalidReport("criterion_score_required")
		}
	}
	transcripts := map[int32]string{}
	for _, v := range r.Responses {
		if strings.TrimSpace(v.Feedback) == "" || len(v.Transcript) > 12000 || len(v.Feedback) > 3000 {
			return r, invalidReport("response_fields")
		}
		if v.Position < 0 || int(v.Position) >= len(s.Turns) {
			return r, invalidReport("response_position")
		}
		if _, ok := transcripts[v.Position]; ok {
			return r, invalidReport("response_duplicate")
		}
		if s.Turns[v.Position].Skipped && strings.TrimSpace(v.Transcript) != "" {
			return r, invalidReport("skipped_response_transcript")
		}
		transcripts[v.Position] = v.Transcript
	}
	for _, v := range r.Issues {
		if !allowed[v.Category] {
			return r, invalidReport("issue_category")
		}
		if v.Position < 0 || int(v.Position) >= len(s.Turns) {
			return r, invalidReport("issue_position")
		}
		if s.Turns[v.Position].Skipped {
			return r, invalidReport("issue_for_skipped_response")
		}
		if v.Second < 0 || math.IsNaN(v.Second) || math.IsInf(v.Second, 0) || v.Second > float64(s.Turns[v.Position].DurationMS)/1000 {
			return r, invalidReport("issue_timestamp")
		}
		if strings.TrimSpace(v.Quote) == "" || !strings.Contains(transcripts[v.Position], v.Quote) {
			return r, invalidReport("issue_quote")
		}
		if strings.TrimSpace(v.Explanation) == "" || strings.TrimSpace(v.Suggestion) == "" {
			return r, invalidReport("issue_fields")
		}
	}
	for _, v := range append(append([]string{}, r.Strengths...), r.NextSteps...) {
		if strings.TrimSpace(v) == "" || len(v) > 2000 {
			return r, invalidReport("list_item")
		}
	}
	duration, answered := int32(0), 0
	parts, plannedParts := map[int32]bool{}, map[int32]bool{}
	for _, t := range s.Turns {
		plannedParts[t.Part] = true
		if !t.Skipped && t.DurationMS > 0 {
			duration += t.DurationMS
			answered++
			parts[t.Part] = true
		}
	}
	insufficient := duration < 60000 || answered < 3 || len(parts) < 3
	if len(plannedParts) == 1 {
		insufficient = duration < 10000 || answered < 1 || len(parts) < 1
	}
	if insufficient {
		r.Assessable = false
		if len(plannedParts) == 1 {
			r.Summary = "There is too little recorded speech in this focused part for a reliable estimate. " + r.Summary
		} else {
			r.Summary = "There is too little recorded speech across all three parts for a reliable band estimate. " + r.Summary
		}
	}
	if r.Assessable {
		overall := math.Round(sum/4*2) / 2
		r.Overall = &overall
	} else {
		r.Overall = nil
		for i := range r.Criteria {
			r.Criteria[i].Score = nil
		}
	}
	return r, nil
}

func invalidReport(reason string) error {
	return fmt.Errorf("invalid speaking feedback: %s", reason)
}

// RecordingBinding is the additional data a stored answer is sealed against.
// It lives here so the two packages that open recordings -- the student's own
// playback and a centre marker's -- cannot drift apart: a binding that differs
// by one character decrypts nothing.
func RecordingBinding(sessionID string, position int32) string {
	return "mockai:speaking:recording:v1:" + sessionID + ":" + strconv.Itoa(int(position))
}
