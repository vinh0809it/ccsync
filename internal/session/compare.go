package session

import (
	"bufio"
	"bytes"
	"encoding/json"
)

// Relation says how one copy of a session's transcript relates to another.
//
// Claude Code gives every conversation entry (user, assistant, system,
// attachment) a unique, path-independent uuid and only ever appends, so a
// later copy of a session holds every entry of an earlier one. Comparing
// the sets of uuids therefore orders copies the way git orders commits,
// even when they were relocated to different paths.
type Relation int

const (
	// Same: both copies hold the same entries (metadata such as a title may
	// still differ).
	Same Relation = iota
	// Ahead: this copy has every entry of the other, and more.
	Ahead
	// Behind: the other copy has every entry of this one, and more.
	Behind
	// Diverged: each copy has entries the other lacks — the session was
	// continued on two machines.
	Diverged
)

func (r Relation) String() string {
	return [...]string{"same", "ahead", "behind", "diverged"}[r]
}

// Compare returns how transcript a relates to transcript b.
func Compare(a, b []byte) Relation {
	ua, ub := entryIDs(a), entryIDs(b)
	aHasExtra, bHasExtra := false, false
	for id := range ua {
		if _, ok := ub[id]; !ok {
			aHasExtra = true
			break
		}
	}
	for id := range ub {
		if _, ok := ua[id]; !ok {
			bHasExtra = true
			break
		}
	}
	switch {
	case aHasExtra && bHasExtra:
		return Diverged
	case aHasExtra:
		return Ahead
	case bHasExtra:
		return Behind
	default:
		return Same
	}
}

// entryIDs returns the uuids of a transcript's conversation entries.
func entryIDs(transcript []byte) map[string]struct{} {
	ids := map[string]struct{}{}
	sc := bufio.NewScanner(bytes.NewReader(transcript))
	sc.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"uuid"`)) {
			continue
		}
		var e struct {
			UUID string `json:"uuid"`
		}
		if json.Unmarshal(line, &e) == nil && e.UUID != "" {
			ids[e.UUID] = struct{}{}
		}
	}
	return ids
}
