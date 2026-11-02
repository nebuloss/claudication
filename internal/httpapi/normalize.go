package httpapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"claudication/internal/api"
	anthropicapi "claudication/internal/api/anthropic"
	geminiapi "claudication/internal/api/gemini"
	"claudication/internal/api/openai"
	"claudication/internal/httpapi/admin"
)

// Reading a recorded body into Anthropic Messages, for the admin UI to draw
// every API's content one way (internal/service/capture). Here, in the root,
// because it takes the dialects themselves, which only the root and the
// gateway may import — and because it must be their translations and no
// copy of them: what the conversation view shows is what the gateway itself
// understood a request to be, which is the point when a translation is the
// suspect.
//
// A request is read with its dialect's Decode — the translation into
// Messages every dialect has. An answer in Messages is gathered from its
// stream; one from ChatGPT or Code Assist goes through the Messages
// surface's own reading of that backend, run on the recorded bytes as the
// relay would run it on the live ones.

func (s *Server) normalizer() admin.Normalizer {
	return func(dialect string, request []byte, status int, header http.Header, answer []byte) admin.Normalized {
		var out admin.Normalized
		req, err := s.requestAsMessages(dialect, request)
		if err != nil {
			out.RequestError = err.Error()
		} else {
			out.Request = req
		}
		if answer == nil {
			return out
		}
		if req == nil {
			out.AnswerError = "its request could not be read, and the answer is read through it"
			return out
		}
		ans, err := s.answerAsMessages(dialect, req, status, header, answer)
		if err != nil {
			out.AnswerError = err.Error()
		} else {
			out.Answer = ans
		}
		return out
	}
}

// requestAsMessages is a request in its dialect read into Messages.
func (s *Server) requestAsMessages(dialect string, body []byte) (json.RawMessage, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errors.New("it has no body")
	}
	switch dialect {
	case anthropicapi.ID:
		if !json.Valid(body) {
			return nil, errors.New("not JSON")
		}
		return body, nil
	case geminiapi.ID:
		body = geminiEnvelope(body)
	}
	p, ok := s.protocols.Find(dialect)
	if !ok {
		return nil, fmt.Errorf("no API named %q", dialect)
	}
	ex, err := p.Decode(body)
	if err != nil {
		return nil, err
	}
	if _, ok := ex.(api.Answerer); ok {
		return nil, errors.New("a request the gateway answers itself (a token count)")
	}
	return ex.Request(), nil
}

// geminiEnvelope makes a Gemini body the envelope the dialect reads: the
// client's leg is one already (geminiapi.Envelope), and Code Assist's
// {"model", "project", "request"} becomes one asking for an answer.
func geminiEnvelope(body []byte) []byte {
	var probe struct {
		Model   string          `json:"model"`
		Method  string          `json:"method"`
		Request json.RawMessage `json:"request"`
	}
	if json.Unmarshal(body, &probe) != nil || probe.Method != "" || probe.Request == nil {
		return body
	}
	return geminiapi.Envelope(probe.Model, "generateContent", probe.Request)
}

// answerAsMessages is an answer in its dialect read into one Messages
// message. req is its request in Messages, which a translated answer is read
// through: the tool names it was sent with, and whether it streamed.
func (s *Server) answerAsMessages(dialect string, req []byte, status int, header http.Header, answer []byte) (json.RawMessage, error) {
	if status >= 400 {
		return nil, errors.New("an error, not an answer: Raw has its words")
	}
	switch dialect {
	case anthropicapi.ID:
		return gatherMessages(answer)
	case openai.ID:
		return s.throughMessages(openai.ID, req, status, header, answer)
	case geminiapi.ID:
		return s.throughMessages(geminiapi.ID, req, status, header, codeAssistWrapped(answer))
	}
	return nil, fmt.Errorf("no API named %q", dialect)
}

// throughMessages reads a backend's answer the way the Messages surface
// does for a caller that asked it in Messages, unstreamed, so it comes out
// one message.
func (s *Server) throughMessages(dialect string, req []byte, status int, header http.Header, answer []byte) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(req, &fields); err != nil {
		return nil, err
	}
	fields["stream"] = json.RawMessage("false")
	req, _ = json.Marshal(fields)

	p, ok := s.protocols.Find(anthropicapi.ID)
	tr, isTranslator := p.(api.Translator)
	if !ok || !isTranslator {
		return nil, errors.New("the Messages API cannot translate")
	}
	ex, err := tr.Translate(req, api.Target{Dialect: dialect, Route: "/v1/messages"})
	if err != nil {
		return nil, err
	}
	rec := &recorder{header: http.Header{}}
	sink := ex.Sink(rec)
	for k, vs := range header {
		sink.Header()[k] = vs
	}
	sink.WriteHeader(status)
	_, _ = sink.Write(answer)
	sink.Close(nil)
	if rec.status >= 400 {
		return nil, fmt.Errorf("the gateway's reading of it failed: %s", bytes.TrimSpace(rec.body.Bytes()))
	}
	if !json.Valid(rec.body.Bytes()) {
		return nil, errors.New("the gateway's reading of it is not one message")
	}
	return rec.body.Bytes(), nil
}

// codeAssistWrapped gives a Gemini answer Code Assist's wrapping, which the
// Messages surface reads: each chunk {"response": …}. An upstream leg's
// answer has it; a client's leg was sent it unwrapped, as one JSON response
// or as a stream of chunks.
func codeAssistWrapped(answer []byte) []byte {
	trimmed := bytes.TrimSpace(answer)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var whole map[string]json.RawMessage
		if json.Unmarshal(trimmed, &whole) == nil {
			if _, wrapped := whole["response"]; wrapped {
				return []byte("data: " + string(trimmed) + "\n\n")
			}
			return []byte(`data: {"response":` + string(trimmed) + "}\n\n")
		}
	}
	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(answer))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		data, ok := bytes.CutPrefix(line, []byte("data:"))
		if !ok {
			out.Write(line)
			out.WriteByte('\n')
			continue
		}
		data = bytes.TrimSpace(data)
		var chunk map[string]json.RawMessage
		if json.Unmarshal(data, &chunk) == nil {
			if _, wrapped := chunk["response"]; !wrapped {
				data = []byte(`{"response":` + string(data) + "}")
			}
		}
		out.WriteString("data: ")
		out.Write(data)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

// gatherMessages is a Messages answer as one message: as it is when it was
// not streamed, gathered from its events when it was.
func gatherMessages(answer []byte) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(answer)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var m struct {
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(trimmed, &m) != nil || m.Content == nil {
			return nil, errors.New("not a model's answer")
		}
		return trimmed, nil
	}

	var msg map[string]any
	var blocks []map[string]any
	partial := map[int]*bytes.Buffer{}
	sc := bufio.NewScanner(bytes.NewReader(answer))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		data, ok := bytes.CutPrefix(sc.Bytes(), []byte("data:"))
		if !ok {
			continue
		}
		var ev struct {
			Type         string         `json:"type"`
			Index        int            `json:"index"`
			Message      map[string]any `json:"message"`
			ContentBlock map[string]any `json:"content_block"`
			Delta        map[string]any `json:"delta"`
			Usage        map[string]any `json:"usage"`
		}
		if json.Unmarshal(bytes.TrimSpace(data), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			msg = ev.Message
		case "content_block_start":
			for len(blocks) <= ev.Index {
				blocks = append(blocks, nil)
			}
			blocks[ev.Index] = ev.ContentBlock
		case "content_block_delta":
			if ev.Index >= len(blocks) || blocks[ev.Index] == nil {
				continue
			}
			b := blocks[ev.Index]
			switch ev.Delta["type"] {
			case "text_delta":
				b["text"] = str(b["text"]) + str(ev.Delta["text"])
			case "thinking_delta":
				b["thinking"] = str(b["thinking"]) + str(ev.Delta["thinking"])
			case "signature_delta":
				b["signature"] = str(b["signature"]) + str(ev.Delta["signature"])
			case "input_json_delta":
				if partial[ev.Index] == nil {
					partial[ev.Index] = &bytes.Buffer{}
				}
				partial[ev.Index].WriteString(str(ev.Delta["partial_json"]))
			}
		case "content_block_stop":
			if buf := partial[ev.Index]; buf != nil && ev.Index < len(blocks) && blocks[ev.Index] != nil {
				var input any
				if json.Unmarshal(buf.Bytes(), &input) == nil {
					blocks[ev.Index]["input"] = input
				} else {
					blocks[ev.Index]["input"] = buf.String()
				}
			}
		case "message_delta":
			if msg == nil {
				continue
			}
			for k, v := range ev.Delta {
				msg[k] = v
			}
			if ev.Usage != nil {
				usage, _ := msg["usage"].(map[string]any)
				if usage == nil {
					usage = map[string]any{}
				}
				for k, v := range ev.Usage {
					usage[k] = v
				}
				msg["usage"] = usage
			}
		}
	}
	if msg == nil {
		return nil, errors.New("no message in the stream")
	}
	content := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		if b != nil {
			content = append(content, b)
		}
	}
	msg["content"] = content
	return json.Marshal(msg)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// recorder is the writer a translated answer is read into.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(p)
}

func (r *recorder) Flush() {}
