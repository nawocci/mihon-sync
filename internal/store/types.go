package store

import "encoding/json"

// Entity identity is stable across devices: database ids differ per device, so
// manga are keyed by (source_id, url), chapters by (manga, chapter url) and
// categories by name — the same rules the app's backup restore uses.
//
// Nullable timestamps travel as 0 on the wire (Go has no int-null in a plain
// struct) and are translated to NULL in SQL. A 0 therefore means "unknown",
// which the merge rules treat as "never overwrite a known value".

type Manga struct {
	SourceID       int64
	URL            string
	Title          string
	Favorite       bool
	FavoriteAt     int64
	ReaderFlags    int64
	ChapterFlags   int64
	UpdateStrategy string
	Notes          string
	Memo           json.RawMessage
	SyncVersion    int64
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
	DateFetch     int64
	DateUpload    int64
	Memo          json.RawMessage
	SyncVersion   int64
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
	ReadAt        int64
	ReadDuration  int64
	Rev           int64
}

type ExcludedScanlator struct {
	MangaSourceID int64
	MangaURL      string
	Scanlator     string
	Rev           int64
	Deleted       bool
}

// Preference is a typed key/value pair. Value is JSON-encoded and interpreted
// according to Type: "int", "long", "float", "string", "boolean", "stringset".
type Preference struct {
	Key     string
	Type    string
	Value   json.RawMessage
	Rev     int64
	Deleted bool
}

type ExtensionStore struct {
	IndexURL         string
	Name             string
	BadgeLabel       string
	SigningKey       string
	ContactWebsite   string
	ContactDiscord   string
	IsLegacy         bool
	ExtensionListURL string
	Rev              int64
	Deleted          bool
}

// ChangeSet is one batch of entity changes, used both for push (client →
// server) and pull (server → client).
type ChangeSet struct {
	Mangas             []Manga
	Chapters           []Chapter
	Categories         []Category
	MangaCategories    []MangaCategory
	History            []HistoryEntry
	ExcludedScanlators []ExcludedScanlator
	Preferences        []Preference
	ExtensionStores    []ExtensionStore
}

func (cs *ChangeSet) Empty() bool {
	return len(cs.Mangas) == 0 && len(cs.Chapters) == 0 && len(cs.Categories) == 0 &&
		len(cs.MangaCategories) == 0 && len(cs.History) == 0 &&
		len(cs.ExcludedScanlators) == 0 && len(cs.Preferences) == 0 &&
		len(cs.ExtensionStores) == 0
}

// Count returns the total number of entities in the batch.
func (cs *ChangeSet) Count() int {
	return len(cs.Mangas) + len(cs.Chapters) + len(cs.Categories) + len(cs.MangaCategories) +
		len(cs.History) + len(cs.ExcludedScanlators) + len(cs.Preferences) + len(cs.ExtensionStores)
}

type Account struct {
	ID        int64
	KeyHash   string
	Label     string
	Rev       int64
	CreatedAt int64
}

type Status struct {
	Rev                    int64
	MangaCount             int64
	ChapterCount           int64
	CategoryCount          int64
	HistoryCount           int64
	PreferenceCount        int64
	ExtensionStoreCount    int64
	ExcludedScanlatorCount int64
	DeviceCount            int64
	AccountCreatedAt       int64
}
