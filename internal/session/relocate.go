package session

import (
	"bytes"
	"encoding/json"
	"strings"
)

// jsonString is s as a JSON string body (no quotes), escaped the way
// Claude Code's JSON.stringify writes it.
func jsonString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	out := strings.TrimSpace(buf.String())
	return out[1 : len(out)-1]
}

// RewriteCwd moves the structural cwd fields from oldCwd to newCwd. Both
// the project root and its subdirectories ("cwd":"<old>/sub") are
// rewritten; paths inside message content are deliberately left alone.
func RewriteCwd(data []byte, oldCwd, newCwd string) []byte {
	if oldCwd == "" || oldCwd == newCwd {
		return data
	}
	oldP := []byte(`"cwd":"` + jsonString(oldCwd))
	newP := []byte(`"cwd":"` + jsonString(newCwd))
	// One pass, so a destination inside the old path is never rewritten
	// twice.
	var out bytes.Buffer
	for {
		i := bytes.Index(data, oldP)
		if i < 0 {
			out.Write(data)
			return out.Bytes()
		}
		rest := data[i+len(oldP):]
		if bytes.HasPrefix(rest, []byte(`"`)) || bytes.HasPrefix(rest, []byte(`/`)) || bytes.HasPrefix(rest, []byte(`\\`)) {
			out.Write(data[:i])
			out.Write(newP)
		} else {
			out.Write(data[:i+len(oldP)])
		}
		data = rest
	}
}

// RewritePath replaces every occurrence of the JSON-encoded oldPath with
// newPath. Used for the session's companion directory, whose path (ending
// in the session uuid) is specific enough to rewrite anywhere — Claude
// Code points at it from message content ("Full output saved to: ...").
func RewritePath(data []byte, oldPath, newPath string) []byte {
	if oldPath == "" || oldPath == newPath {
		return data
	}
	return bytes.ReplaceAll(data, []byte(jsonString(oldPath)), []byte(jsonString(newPath)))
}
