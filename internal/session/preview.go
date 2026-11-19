package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
)

// Message is one user or assistant text from a transcript, for a preview.
type Message struct {
	Role string    `json:"role"` // "user" or "assistant"
	Text string    `json:"text"`
	Time time.Time `json:"time,omitzero"`
}

// previewChars caps one previewed message.
const previewChars = 600

// Preview returns the last n user and assistant texts of a transcript.
// Tool calls and results, thinking, and wrapped command or system text
// are left out. The line format is Claude Code's and may change; lines it
// can't read are skipped.
func Preview(transcript io.Reader, n int) ([]Message, error) {
	type line struct {
		Type      string    `json:"type"`
		IsMeta    bool      `json:"isMeta"`
		Summary   bool      `json:"isCompactSummary"` // Claude Code's recap after /compact
		Timestamp time.Time `json:"timestamp"`
		Message   struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	var out []Message
	sc := bufio.NewScanner(transcript)
	sc.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		b := sc.Bytes()
		if !bytes.Contains(b, []byte(`"user"`)) && !bytes.Contains(b, []byte(`"assistant"`)) { // cheap pre-filter
			continue
		}
		var l line
		if json.Unmarshal(b, &l) != nil || l.IsMeta || l.Summary || (l.Type != "user" && l.Type != "assistant") {
			continue
		}
		text := strings.TrimSpace(contentText(l.Message.Content))
		if text == "" || strings.HasPrefix(text, "<") {
			continue
		}
		if r := []rune(text); len(r) > previewChars {
			text = string(r[:previewChars]) + "…"
		}
		out = append(out, Message{Role: l.Type, Text: text, Time: l.Timestamp})
		if len(out) > n {
			out = out[1:]
		}
	}
	return out, sc.Err()
}

// contentText joins the text parts of a message's content, which is a
// string or a list of typed parts.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var texts []string
	for _, p := range parts {
		if p.Type == "text" && strings.TrimSpace(p.Text) != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// PreviewFile previews a local transcript from its last few megabytes.
func PreviewFile(file string, n int) ([]Message, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	const window = 4 << 20
	if info, err := f.Stat(); err == nil && info.Size() > window {
		if _, err := f.Seek(info.Size()-window, io.SeekStart); err != nil {
			return nil, err
		}
	}
	return Preview(f, n) // a line cut by the window doesn't parse and is skipped
}
