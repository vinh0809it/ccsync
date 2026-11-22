package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

// Relation says how one copy of a session's transcript relates to another.
//
// Claude Code gives every conversation entry (user, assistant, system,
// attachment) a unique, path-independent uuid and only ever appends — also
// when compacting — so a later copy of a session holds every entry of an
// earlier one. Comparing the sets of entries therefore orders copies the
// way git orders commits, even when they were relocated to different
// paths. Renames (custom-title lines) count as entries too, so a rename
// alone also syncs in order.
type Relation int

const (
	// Same: both copies hold the same entries; their bytes may still differ
	// in relocated paths or other bookkeeping lines.
	Same Relation = iota
	// Ahead: this copy has every entry of the other, and more.
	Ahead
	// Behind: the other copy has every entry of this one, and more.
	Behind
	// Diverged: each copy has entries the other lacks — the session was
	// continued on two machines.
	Diverged
	// Unknown: a copy has no recognizable entries (an unexpected transcript
	// format), so there is no evidence to order them by.
	Unknown
)

func (r Relation) String() string {
	return [...]string{"same", "ahead", "behind", "diverged", "unknown"}[r]
}

// Compare returns how transcript a relates to transcript b.
func Compare(a, b []byte) Relation { return compareIDs(entryIDs(a), entryIDs(b)) }

// EntrySet records a transcript's entries compactly enough to keep in a
// store's metadata: a short hash of each, sorted. CompareSet relates
// another transcript to it as Compare would to the transcript itself, so
// sync status needs no download. "" for a transcript without entries.
func EntrySet(transcript []byte) string {
	hashed := hashIDs(entryIDs(transcript))
	return strings.Join(slices.Sorted(maps.Keys(hashed)), "")
}

// CompareSet returns how transcript a relates to the transcript whose
// EntrySet is set.
func CompareSet(a []byte, set string) Relation {
	ub := map[string]struct{}{}
	for i := 0; i+entryHashLen <= len(set); i += entryHashLen {
		ub[set[i:i+entryHashLen]] = struct{}{}
	}
	return compareIDs(hashIDs(entryIDs(a)), ub)
}

// entryHashLen is the hex length of an entry's hash in an EntrySet: 48
// bits, so a clash among a session's entries is vanishingly unlikely.
const entryHashLen = 12

func hashIDs(ids map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for id := range ids {
		sum := sha256.Sum256([]byte(id))
		out[hex.EncodeToString(sum[:entryHashLen/2])] = struct{}{}
	}
	return out
}

func compareIDs(ua, ub map[string]struct{}) Relation {
	if len(ua) == 0 || len(ub) == 0 {
		return Unknown
	}
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

// Fingerprint identifies a transcript's set of entries: two transcripts
// with entries have equal fingerprints exactly when Compare finds them
// Same. A transcript without entries has none ("").
func Fingerprint(transcript []byte) string {
	ids := entryIDs(transcript)
	if len(ids) == 0 {
		return ""
	}
	h := sha256.New()
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		h.Write([]byte(id))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// entryIDs returns the identities of a transcript's entries: conversation
// entries by uuid, renames by their title.
func entryIDs(transcript []byte) map[string]struct{} {
	ids := map[string]struct{}{}
	for rest := transcript; len(rest) > 0; {
		// Lines can be many megabytes (inline images); no length limit.
		line := rest
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			line, rest = rest[:i], rest[i+1:]
		} else {
			rest = nil
		}
		if !bytes.Contains(line, []byte(`"uuid"`)) && !bytes.Contains(line, []byte(`"custom-title"`)) {
			continue
		}
		var e struct {
			Type        string `json:"type"`
			UUID        string `json:"uuid"`
			CustomTitle string `json:"customTitle"`
		}
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		switch {
		case e.UUID != "":
			ids["uuid:"+e.UUID] = struct{}{}
		case e.Type == "custom-title":
			ids["title:"+e.CustomTitle] = struct{}{}
		}
	}
	return ids
}
