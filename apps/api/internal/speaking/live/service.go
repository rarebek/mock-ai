package live

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/coder/websocket"
	d "github.com/own/mock-ai/apps/api/internal/speaking/domain"
	"log/slog"
	"strings"
	"time"
)

type Repository interface {
	ClaimLive(context.Context, string, string) (d.Session, error)
	RenewLive(context.Context, d.Session) error
	ReleaseLive(context.Context, d.Session) error
	SaveLive(context.Context, d.Session, int32, []byte, string, bool, string) error
	SkipLive(context.Context, d.Session, int32, string) error
	Queue(context.Context, string, string) (d.Session, error)
	CreateLive(context.Context, string, string, string, d.Examiner, string, []d.Turn) (d.Session, error)
}
type Provider interface {
	ConnectLive(context.Context) (*websocket.Conn, error)
}
type Service struct {
	Repo     Repository
	Provider Provider
	Model    string
	Log      *slog.Logger
	// Settle is how long a student may stay quiet after a substantive answer
	// before the examiner is asked to decide. Zero uses the production default.
	Settle time.Duration
}
type packet struct {
	kind       websocket.MessageType
	data       []byte
	err        error
	generation int
}

func read(ctx context.Context, c *websocket.Conn, ch chan<- packet, generation int) {
	for {
		kind, b, e := c.Read(ctx)
		select {
		case ch <- packet{kind: kind, data: b, err: e, generation: generation}:
		case <-ctx.Done():
			return
		}
		if e != nil {
			return
		}
	}
}
func write(ctx context.Context, c *websocket.Conn, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.Write(ctx, websocket.MessageText, b)
}

// Run owns progression. Provider output cannot advance the persisted interview
// without a validated tool call and actual recorded speech.
func (s *Service) Run(parent context.Context, client *websocket.Conn, session d.Session) (runErr error) {
	started := time.Now()
	position, currentPhase := 0, "starting"
	defer func() {
		if runErr != nil && s.Log != nil {
			s.Log.Warn("live speaking stream ended", "session_id", session.ID, "position", position, "phase", currentPhase, "reason", terminationReason(runErr), "duration_ms", time.Since(started).Milliseconds())
		}
	}()
	ctx, cancel := context.WithTimeout(parent, 20*time.Minute)
	defer cancel()
	defer func() {
		c, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = s.Repo.ReleaseLive(c, session)
	}()
	upstream, e := s.Provider.ConnectLive(ctx)
	if e != nil {
		if s.Log != nil {
			s.Log.Warn("live speaking upstream connection failed", "session_id", session.ID, "stage", "initial")
		}
		return e
	}
	defer func() { upstream.CloseNow() }()
	pos := 0
	for pos < len(session.Turns) && session.Turns[pos].Answered {
		pos++
	}
	if pos == len(session.Turns) {
		_, e = s.Repo.Queue(ctx, session.UserID, session.ID)
		return e
	}
	if e = write(ctx, upstream, Setup(session, pos, "")); e != nil {
		return e
	}
	client.SetReadLimit(8192)
	incoming, provider := make(chan packet, 8), make(chan packet, 8)
	providerGeneration := 1
	go read(ctx, client, incoming, 0)
	go read(ctx, upstream, provider, providerGeneration)
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	phase := "connecting"
	currentPhase = phase
	allowOutput := true
	outputBytes, outputRetries := 0, 0
	awaitPlayback := false
	prepared := false
	finished := false
	var prepUntil time.Time
	var pcm []byte
	transcript := ""
	resumeHandle := ""
	recoveries := 0
	helpStart := 0
	helpSpeech := ""
	audioStarted := time.Now()
	received := 0
	lastControl := time.Time{}
	// Turn-taking safety net. The model decides when a turn ends, but if no
	// tool call ever arrives the session hangs in "listening" forever, which
	// is what a stalled interview looks like to a student.
	lastVoiceAt := time.Time{}
	nudges := 0
	nudgedAt := time.Time{}
	// Ingress accounting: whether the student's audio arrives at all, and
	// whether it carries any signal, is the first thing to know when an
	// interview stalls. Nothing recorded it before.
	var peak, clientRMS, clientPeak float64
	loudChunks := 0
	reportedAt := time.Time{}
	// How much of this turn was actually speech. Turn-taking used to key off
	// the transcript, and production returned 7 characters for a 27-second
	// turn: every guard below sat waiting for text that never came and the
	// interview deadlocked. Measured loudness is the reliable signal.
	voicedMs := 0
	clearVoicing := func() { voicedMs, peak, loudChunks = 0, 0, 0 }
	spokenQuestion := func() string {
		if helpSpeech != "" {
			return helpSpeech
		}
		if finished {
			return "Thank you. That completes your interview. Your report will be prepared shortly."
		}
		if session.Turns[pos].Part == 2 && prepared {
			return "Your preparation time is over. Please begin your two-minute answer now."
		}
		return session.Turns[pos].Prompt
	}
	state := func() error {
		position, currentPhase = pos, phase
		return write(ctx, client, map[string]any{"type": "state", "phase": phase, "position": pos, "part": session.Turns[pos].Part, "question": session.Turns[pos].Prompt, "prepare_until": prepUntil.UnixMilli()})
	}
	reconnectUpstream := func(reason string) error {
		if recoveries >= 6 {
			if s.Log != nil {
				s.Log.Error("live speaking upstream recovery exhausted", "session_id", session.ID, "position", pos, "reason", reason, "recoveries", recoveries)
			}
			return d.ErrUnavailable
		}
		recoveries++
		if s.Log != nil {
			s.Log.Warn("live speaking upstream reconnect", "session_id", session.ID, "position", pos, "reason", reason, "attempt", recoveries, "resumable", resumeHandle != "")
		}
		providerGeneration++
		upstream.CloseNow()
		var next *websocket.Conn
		var connectErr error
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(attempt) * 300 * time.Millisecond):
				}
			}
			next, connectErr = s.Provider.ConnectLive(ctx)
			if connectErr == nil {
				break
			}
		}
		if connectErr != nil {
			if s.Log != nil {
				s.Log.Error("live speaking upstream reconnect failed", "session_id", session.ID, "position", pos, "reason", reason, "attempts", 3)
			}
			return d.ErrUnavailable
		}
		upstream = next
		go read(ctx, upstream, provider, providerGeneration)
		pcm = nil
		transcript = ""
		helpStart = 0
		clearVoicing()
		helpSpeech = ""
		prepared = false
		prepUntil = time.Time{}
		allowOutput = true
		outputBytes, outputRetries = 0, 0
		awaitPlayback = false
		phase = "connecting"
		if e := state(); e != nil {
			return e
		}
		return write(ctx, upstream, Setup(session, pos, resumeHandle))
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-heartbeat.C:
			if e = s.Repo.RenewLive(ctx, session); e != nil {
				return e
			}
			if e = write(ctx, client, map[string]string{"type": "heartbeat"}); e != nil {
				return e
			}
		case <-tick.C:
			// A student who pauses mid-thought must never be rushed, so the nudge
			// only fires once they have actually produced a substantive answer and
			// then gone quiet. Part 2 is a two-minute long turn built out of
			// pauses, so it is given far more room than Parts 1 and 3.
			if phase == "listening" && s.Log != nil && time.Since(reportedAt) >= 5*time.Second {
				reportedAt = time.Now()
				s.Log.Info("live speaking ingress", "session_id", session.ID, "position", pos,
					"client_audio_bytes", received, "turn_pcm_bytes", len(pcm),
					"peak_rms", int(peak*1000), "chunks_with_signal", loudChunks,
					"browser_rms", int(clientRMS*1000), "browser_peak_rms", int(clientPeak*1000),
					"voiced_ms", voicedMs,
					"transcript_chars", len(strings.TrimSpace(transcript[helpStart:])))
			}
			// Substantive enough to be a finished answer rather than a pause: two
			// seconds of speech, or a transcript with real words in it. Either is
			// sufficient; requiring both is what stalled the interview.
			answered := voicedMs >= 2000 || Meaningful(transcript[helpStart:])
			if phase == "listening" && !lastVoiceAt.IsZero() && nudges < 2 && answered {
				settle := s.Settle
				if settle <= 0 {
					settle = 3 * time.Second
				}
				if session.Turns[pos].Part == 2 {
					// The Part 2 long turn is built out of pauses; it gets far more room.
					settle = settle * 7 / 3
				}
				quiet := time.Since(lastVoiceAt)
				since := time.Since(nudgedAt)
				if nudges > 0 {
					since = time.Since(nudgedAt)
				}
				due := nudges == 0 && quiet >= settle
				if nudges == 1 && since >= settle+2*time.Second {
					due = true
				}
				if due {
					nudges++
					nudgedAt = time.Now()
					note := "The student has been silent for " + jsonNumber(int(quiet.Seconds())) + " seconds after speaking. " +
						"Decide now: call advance_question if their answer is complete, or wait_for_student if they are still mid-thought. Do not speak before the tool."
					if nudges == 2 {
						note = "The student has now been silent for " + jsonNumber(int(quiet.Seconds())) + " seconds. " +
							"You must call a tool now: advance_question if the answer is complete, wait_for_student if it is not."
					}
					if s.Log != nil {
						s.Log.Info("live speaking silence nudge", "session_id", session.ID, "position", pos,
							"part", session.Turns[pos].Part, "quiet_ms", quiet.Milliseconds(), "attempt", nudges)
					}
					if e = write(ctx, upstream, Control(note)); e != nil {
						return e
					}
				}
			}
			// Both nudges ignored. Production showed the examiner answering
			// wait_for_student twice and then sitting in silence while the student
			// waited for it — a deadlock in which each side waits for the other.
			// A real examiner moves on. After a long silence following an answer
			// that is both substantive and audible, the server advances using the
			// configured next question, never one the model proposed.
			if phase == "listening" && nudges >= 2 && !lastVoiceAt.IsZero() && answered {
				// Derived from the same base as the nudges so the whole ladder
				// scales together and stays testable: 15s on Parts 1 and 3, 35s on
				// the Part 2 long turn.
				hard := s.Settle
				if hard <= 0 {
					hard = 3 * time.Second
				}
				if session.Turns[pos].Part == 2 {
					hard = hard * 7 / 3
				}
				hard *= 5
				if _, audioErr := d.ValidateAudio(Wave(pcm), session.Turns[pos].Seconds); audioErr == nil && time.Since(lastVoiceAt) >= hard {
					final := pos == len(session.Turns)-1
					next := ""
					if !final {
						next = session.Turns[pos+1].Prompt
					}
					if s.Log != nil {
						s.Log.Warn("live speaking advanced on silence", "session_id", session.ID, "position", pos,
							"part", session.Turns[pos].Part, "quiet_ms", time.Since(lastVoiceAt).Milliseconds(),
							"transcript_chars", len(strings.TrimSpace(transcript[helpStart:])), "final", final)
					}
					if e = s.Repo.SaveLive(ctx, session, int32(pos), Wave(pcm), transcript, true, next); e != nil {
						return e
					}
					session.Turns[pos].Answered = true
					session.Turns[pos].Transcript = transcript
					pcm, transcript, helpStart = nil, "", 0
					clearVoicing()
					allowOutput, outputBytes, outputRetries = true, 0, 0
					lastVoiceAt, nudges, nudgedAt = time.Time{}, 0, time.Time{}
					phase = "examiner"
					instruction := "The student's answer is complete and saved. Thank them briefly and say their report will be prepared."
					if !final {
						pos++
						session.Turns[pos].Prompt = next
						prepared = false
						instruction = "The student's answer is complete and saved. " + nextQuestionInstruction + " The question is: " + next
					} else {
						finished = true
					}
					if e = state(); e != nil {
						return e
					}
					if e = write(ctx, upstream, Control(instruction)); e != nil {
						return e
					}
				}
			}
			if phase == "preparing" && !time.Now().Before(prepUntil) {
				phase = "examiner"
				allowOutput = true
				outputBytes, outputRetries = 0, 0
				prepared = true
				prepUntil = time.Time{}
				if e = state(); e != nil {
					return e
				}
				if e = write(ctx, upstream, Control("Preparation ended. Invite the student to begin their two-minute long turn now; do not ask a new question.")); e != nil {
					return e
				}
			}
		case p := <-incoming:
			if p.err != nil {
				if s.Log != nil {
					s.Log.Info("live speaking client disconnected", "session_id", session.ID, "position", pos, "close_status", websocket.CloseStatus(p.err))
				}
				return p.err
			}
			if p.kind == websocket.MessageBinary {
				if phase != "listening" {
					continue
				}
				if len(p.data)%2 != 0 {
					return d.ErrInvalid
				}
				received += len(p.data)
				if received > int(time.Since(audioStarted).Seconds()*48000)+64000 {
					return d.ErrLimit
				}
				// A hard question budget bounds spend and ends the timed long turn.
				// The examiner moves on at the limit, just as in the real interview.
				if len(pcm)+len(p.data) > int(session.Turns[pos].Seconds)*32000 {
					if e = write(ctx, upstream, map[string]any{"realtimeInput": map[string]bool{"audioStreamEnd": true}}); e != nil {
						return e
					}
					next := ""
					if pos+1 < len(session.Turns) {
						next = session.Turns[pos+1].Prompt
					}
					// Preserve audible speech even if live transcription arrives late;
					// only genuine silence/invalid audio is recorded as skipped.
					_, audioErr := d.ValidateAudio(Wave(pcm), session.Turns[pos].Seconds)
					skipped := audioErr != nil
					if skipped {
						if e = s.Repo.SkipLive(ctx, session, int32(pos), next); e != nil {
							return e
						}
					} else if e = s.Repo.SaveLive(ctx, session, int32(pos), Wave(pcm), transcript, true, next); e != nil {
						return e
					}
					session.Turns[pos].Answered = true
					session.Turns[pos].Skipped = skipped
					session.Turns[pos].Transcript = transcript
					if next == "" {
						if _, e = s.Repo.Queue(ctx, session.UserID, session.ID); e != nil {
							return e
						}
						_ = write(ctx, client, map[string]string{"type": "done"})
						return nil
					}
					pos++
					pcm = nil
					transcript = ""
					helpStart = 0
					clearVoicing()
					prepared = false
					allowOutput = true
					outputBytes, outputRetries = 0, 0
					phase = "examiner"
					if e = state(); e != nil {
						return e
					}
					transition := timeLimitTransition(session.Turns[pos-1].Part, pos, next)
					if e = write(ctx, upstream, Control(transition)); e != nil {
						return e
					}
					continue
				}
				pcm = append(pcm, p.data...)
				rms := rms16(p.data)
				if rms > peak {
					peak = rms
				}
				if rms > 0.01 {
					loudChunks++
				}
				// Voiced relative to this turn's own loudest moment, so a quiet
				// microphone is judged on the same scale as a loud one. Measured
				// silence sits around 0.01 and speech around 0.18, so a quarter of
				// the peak separates them cleanly. The absolute floor keeps a
				// silent room from treating its own hiss as an answer.
				if peak > 0.02 && rms > peak/4 {
					voicedMs += len(p.data) * 1000 / 32000
					lastVoiceAt = time.Now()
					nudges, nudgedAt = 0, time.Time{}
				}
				e = write(ctx, upstream, map[string]any{"realtimeInput": map[string]any{"audio": map[string]string{"data": base64.StdEncoding.EncodeToString(p.data), "mimeType": "audio/pcm;rate=16000"}}})
				if e != nil {
					return e
				}
				continue
			}
			var command struct {
				Type string  `json:"type"`
				RMS  float64 `json:"rms"`
			}
			if json.Unmarshal(p.data, &command) != nil {
				return d.ErrInvalid
			}
			switch command.Type {
			case "mic_level":
				// What the browser hears, to compare against what we receive. If the
				// browser measures speech and the server receives silence, the fault
				// is in the capture worklet; if both are silent, it is the device or
				// the getUserMedia constraints.
				if command.RMS > clientPeak {
					clientPeak = command.RMS
				}
				clientRMS = command.RMS
				continue
			case "playback_done":
				if !awaitPlayback {
					continue
				}
				awaitPlayback = false
				if finished {
					_, e = s.Repo.Queue(ctx, session.UserID, session.ID)
					if e != nil {
						return e
					}
					_ = write(ctx, client, map[string]string{"type": "done"})
					return nil
				}
				allowOutput = false
				helpSpeech = ""
				if session.Turns[pos].Part == 2 && !prepared {
					phase = "preparing"
					prepUntil = time.Now().Add(time.Duration(session.Turns[pos].PrepSeconds) * time.Second)
				} else {
					phase = "listening"
					audioStarted = time.Now()
					received = 0
				}
				if e = state(); e != nil {
					return e
				}
			case "repeat":
				if phase != "listening" || time.Since(lastControl) < 5*time.Second {
					continue
				}
				lastControl = time.Now()
				allowOutput = true
				outputBytes, outputRetries = 0, 0
				phase = "examiner"
				_ = state()
				if e = write(ctx, upstream, Control("Repeat the current question without advancing: "+session.Turns[pos].Prompt)); e != nil {
					return e
				}
			case "done_speaking":
				if phase != "listening" || time.Since(lastControl) < 5*time.Second {
					continue
				}
				lastControl = time.Now()
				if e = write(ctx, upstream, map[string]any{"realtimeInput": map[string]bool{"audioStreamEnd": true}}); e != nil {
					return e
				}
			default:
				return d.ErrInvalid
			}
		case p := <-provider:
			if p.generation != providerGeneration {
				continue
			}
			if p.err != nil {
				status := websocket.CloseStatus(p.err)
				if s.Log != nil {
					s.Log.Warn("live speaking upstream closed", "session_id", session.ID, "close_status", status, "position", pos)
				}
				// Authentication, policy and invalid setup errors cannot be repaired
				// by reconnecting with the same credentials and configuration.
				if permanentClose(status) {
					_ = write(ctx, client, map[string]any{"type": "error", "retryable": false, "message": "Speaking is temporarily unavailable. Your completed answers are saved. Please try again later."})
					return d.ErrUnavailable
				}
				if e = reconnectUpstream("read_closed"); e != nil {
					return e
				}
				continue
			}
			var msg struct {
				SetupComplete           json.RawMessage `json:"setupComplete"`
				GoAway                  json.RawMessage `json:"goAway"`
				SessionResumptionUpdate struct {
					NewHandle string `json:"newHandle"`
					Resumable bool   `json:"resumable"`
				} `json:"sessionResumptionUpdate"`
				ServerContent struct {
					ModelTurn struct {
						Parts []struct {
							InlineData *struct {
								Data     string `json:"data"`
								MimeType string `json:"mimeType"`
							} `json:"inlineData"`
						}
					} `json:"modelTurn"`
					InputTranscription struct {
						Text string `json:"text"`
					} `json:"inputTranscription"`
					TurnComplete bool `json:"turnComplete"`
					Interrupted  bool `json:"interrupted"`
				} `json:"serverContent"`
				ToolCall struct {
					FunctionCalls []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
						Args struct {
							Question    string `json:"question"`
							Explanation string `json:"explanation"`
						} `json:"args"`
					}
				} `json:"toolCall"`
			}
			if json.Unmarshal(p.data, &msg) != nil {
				return d.ErrUnavailable
			}
			if msg.SessionResumptionUpdate.Resumable && msg.SessionResumptionUpdate.NewHandle != "" {
				resumeHandle = msg.SessionResumptionUpdate.NewHandle
			}
			if len(msg.GoAway) > 0 {
				if e = reconnectUpstream("go_away"); e != nil {
					return e
				}
				continue
			}
			if len(msg.SetupComplete) > 0 {
				phase = "examiner"
				if e = state(); e != nil {
					return e
				}
				if e = write(ctx, upstream, Control("Begin or resume at position "+jsonNumber(pos)+". Ask this question aloud now: "+session.Turns[pos].Prompt)); e != nil {
					return e
				}
			}
			content := msg.ServerContent
			if phase == "listening" {
				if strings.TrimSpace(content.InputTranscription.Text) != "" {
					lastVoiceAt = time.Now()
					nudges, nudgedAt = 0, time.Time{}
				}
				transcript += content.InputTranscription.Text
				if len(transcript) > 16000 {
					return d.ErrLimit
				}
			}
			for _, call := range msg.ToolCall.FunctionCalls {
				if s.Log != nil {
					s.Log.Info("live speaking tool call", "session_id", session.ID, "position", pos,
						"part", session.Turns[pos].Part, "phase", phase, "tool", call.Name,
						"transcript_chars", len(strings.TrimSpace(transcript[helpStart:])))
				}
				response := map[string]any{"instruction": "Stay silent and wait for the student to finish."}
				if phase == "examiner" {
					response = map[string]any{"question": spokenQuestion(), "instruction": "The controller has already approved this examiner turn. Speak the supplied words aloud now; do not call another tool or wait for a student answer before asking the question."}
				}
				if phase == "listening" {
					switch call.Name {
					case "repeat_question", "clarify_question":
						// Help requests are conversational turns, never scored answers
						// or permission to advance based on an earlier transcript.
						if strings.TrimSpace(transcript[helpStart:]) == "" {
							break
						}
						if _, err := d.ValidateAudio(Wave(pcm), session.Turns[pos].Seconds); err != nil {
							break
						}
						helpSpeech = session.Turns[pos].Prompt
						if call.Name == "clarify_question" {
							explanation := strings.TrimSpace(call.Args.Explanation)
							if len(explanation) < 5 || len(explanation) > 700 {
								break
							}
							helpSpeech = explanation
						}
						helpStart = len(transcript)
						clearVoicing()
						allowOutput = true
						outputBytes, outputRetries = 0, 0
						phase = "examiner"
						response = map[string]any{"question": helpSpeech, "instruction": helpQuestionInstruction}
					case "advance_question", "finish_interview":
						// The model decided the answer was finished, and it heard the
						// audio. A short transcript is not evidence of a short answer:
						// production returned seven characters for a 27-second answer,
						// and this guard refused the model's correct decision on every
						// question. Accept it when either the text or the measured
						// speech shows the student spoke; ValidateAudio below is still
						// the hard gate on what gets saved.
						valid := Meaningful(transcript[helpStart:]) || voicedMs >= 2000
						refusal := ""
						if !valid {
							refusal = "too_short"
						}
						if _, err := d.ValidateAudio(Wave(pcm), session.Turns[pos].Seconds); err != nil {
							valid, refusal = false, "audio_rejected"
						}
						if !valid {
							if s.Log != nil {
								s.Log.Warn("live speaking advance refused", "session_id", session.ID, "position", pos,
									"tool", call.Name, "reason", refusal,
									"transcript_chars", len(strings.TrimSpace(transcript[helpStart:])))
							}
							// A refusal on noise stays silent: answering a cough aloud is
							// exactly what the turn-taking rules forbid. A refusal on real
							// but too-brief speech gets a way forward instead of a freeze.
							if refusal == "too_short" && spoken(transcript[helpStart:]) > 0 {
								response = map[string]any{"instruction": "That answer was too brief to save. Stay on this question and invite the student to say a little more."}
								// Entering "listening" disables model output, and this path
								// never re-enabled it: the examiner spoke and every byte was
								// discarded below, leaving the student in silence with no
								// way out. Hand the turn to the examiner properly, the way
								// an approved question or a clarification does.
								allowOutput = true
								outputBytes, outputRetries = 0, 0
								phase = "examiner"
							}
						}
						final := pos == len(session.Turns)-1
						if valid && ((final && call.Name == "finish_interview") || (!final && call.Name == "advance_question")) {
							next := ""
							if !final {
								next = strings.TrimSpace(call.Args.Question)
								if session.Turns[pos+1].Part == 2 {
									next = session.Turns[pos+1].Prompt
								}
								if len(next) < 8 || len(next) > 1600 {
									break
								}
							}
							if e = s.Repo.SaveLive(ctx, session, int32(pos), Wave(pcm), transcript, true, next); e != nil {
								return e
							}
							session.Turns[pos].Answered = true
							session.Turns[pos].Transcript = transcript
							pcm = nil
							transcript = ""
							helpStart = 0
							clearVoicing()
							allowOutput = true
							outputBytes, outputRetries = 0, 0
							phase = "examiner"
							if final {
								finished = true
								response = map[string]any{"instruction": "The interview is complete. Thank the student briefly and say their report will be prepared."}
							} else {
								pos++
								session.Turns[pos].Prompt = next
								prepared = false
								response = map[string]any{"position": pos, "question": next, "instruction": nextQuestionInstruction}
							}
						}
					}
				}
				if e = write(ctx, upstream, map[string]any{"toolResponse": map[string]any{"functionResponses": []any{map[string]any{"id": call.ID, "name": call.Name, "response": response}}}}); e != nil {
					return e
				}
				if e = state(); e != nil {
					return e
				}
			}
			if content.Interrupted {
				if e = write(ctx, client, map[string]string{"type": "interrupted"}); e != nil {
					return e
				}
			}
			// Output is disabled while the student holds the turn, so anything the
			// model says then is deliberately discarded. That silence hid a real
			// deadlock for days: the examiner was speaking and nobody could tell.
			if !allowOutput && s.Log != nil {
				for _, part := range content.ModelTurn.Parts {
					if part.InlineData != nil && part.InlineData.Data != "" {
						s.Log.Warn("live speaking examiner audio discarded", "session_id", session.ID, "position", pos, "phase", phase)
						break
					}
				}
			}
			if allowOutput {
				for _, part := range content.ModelTurn.Parts {
					if part.InlineData != nil {
						b, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
						if err != nil {
							return d.ErrUnavailable
						}
						if !strings.HasPrefix(part.InlineData.MimeType, "audio/pcm") {
							return d.ErrUnavailable
						}
						outputBytes += len(b)
						c, stop := context.WithTimeout(ctx, 10*time.Second)
						e = client.Write(c, websocket.MessageBinary, b)
						stop()
						if e != nil {
							return e
						}
					}
				}
			}
			if content.TurnComplete && allowOutput {
				// Some provider turns contain only tool calls. Never tell the
				// browser to start listening when no question was actually spoken.
				if outputBytes < 4800 {
					if outputRetries >= 2 {
						return d.ErrUnavailable
					}
					outputRetries++
					if e = write(ctx, upstream, Control("No audible examiner response was received. Do not call tools. Speak these approved words aloud now: "+spokenQuestion())); e != nil {
						return e
					}
					continue
				}
				awaitPlayback = true
				if e = write(ctx, client, map[string]string{"type": "audio_end"}); e != nil {
					return e
				}
			}
		}
	}
}

func permanentClose(status websocket.StatusCode) bool {
	return status == websocket.StatusPolicyViolation || status == websocket.StatusUnsupportedData || status == websocket.StatusInvalidFramePayloadData
}
func jsonNumber(n int) string { b, _ := json.Marshal(n); return string(b) }

func timeLimitTransition(part int32, position int, next string) string {
	if part == 2 {
		return "The two-minute Part 2 time limit has ended. Tell the student briefly that time is up, introduce Part 3, then ask this question: " + next
	}
	return "The answer time limit has ended. Tell the student briefly that time is up, then ask the next question at position " + jsonNumber(position) + ": " + next
}

func terminationReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "session_deadline"
	case errors.Is(err, context.Canceled):
		return "request_canceled"
	case errors.Is(err, d.ErrInvalid):
		return "invalid_input"
	case errors.Is(err, d.ErrLimit):
		return "limit"
	case errors.Is(err, d.ErrConflict):
		return "lease_conflict"
	case errors.Is(err, d.ErrUnavailable):
		return "upstream_unavailable"
	case websocket.CloseStatus(err) != -1:
		return "websocket_closed"
	default:
		return "internal"
	}
}
