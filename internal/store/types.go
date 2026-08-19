package store

import "encoding/json"

// Entity identity is stable across devices: DB ids differ per device, so
// manga are keyed by (source_id, url), chapters by (manga, chapter url),
// categories by name — mirroring Mihon's backup restore logic.

type Manga struct {
	SourceID       int64
	URL            string
	Title          string
	Favorite       bool
	ChapterFlags   int64
	ViewerFlags    int64
	UpdateStrategy string
	Notes          string
	DateAdded      int64
	ClientVersion  int64
	Rev            int64
	Deleted        bool
}

type Chapter struct {
	MangaSourceID int64
	MangaURL      string
	URL           string
	Read          bool
	Bookmark      bool
	LastPageRead  int64
	ClientVersion int64
	Rev           int64
}

type Category struct {
	Name    string
	Order   int64
	Flags   int64
	Rev     int64
	Deleted bool
}

type MangaCategory struct {
	MangaSourceID int64
	MangaURL      string
	Category      string
	Rev           int64
	Deleted       bool
}

type HistoryEntry struct {
	MangaSourceID int64
	MangaURL      string
	ChapterURL    string
	LastRead      int64
	ReadDuration  int64
	Rev           int64
}

// Preference is a typed key/value pair. Value is JSON-encoded and
// interpreted according to Type: "int", "long", "float", "string",
// "boolean", "stringset".
type Preference struct {
	Key     string
	Type    string
	Value   json.RawMessage
	Rev     int64
	Deleted bool
}

// ChangeSet is one batch of entity changes, used both for push (client →
// server) and pull (server → client).
type ChangeSet struct {
	Mangas          []Manga
	Chapters        []Chapter
	Categories      []Category
	MangaCategories []MangaCategory
	History         []HistoryEntry
	Preferences     []Preference
}

func (cs *ChangeSet) Empty() bool {
	return len(cs.Mangas) == 0 && len(cs.Chapters) == 0 && len(cs.Categories) == 0 &&
		len(cs.MangaCategories) == 0 && len(cs.History) == 0 && len(cs.Preferences) == 0
}

type Account struct {
	ID        int64
	KeyHash   string
	Label     string
	Rev       int64
	CreatedAt int64
}

type Status struct {
	Rev              int64
	MangaCount       int64
	ChapterCount     int64
	CategoryCount    int64
	HistoryCount     int64
	PreferenceCount  int64
	DeviceCount      int64
	AccountCreatedAt int64
}
