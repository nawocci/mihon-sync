package syncapi

import (
	"encoding/json"

	"github.com/nawocci/mihon-sync/internal/store"
)

// Wire DTOs. Entity identity mirrors the app's backup restore: manga are keyed
// by (source_id, url), chapters by (manga, chapter url), categories by name.
//
// Nullable timestamps are sent as null. Clients that predate a field simply
// omit it, which decodes to the zero value.

type mangaDTO struct {
	SourceID       int64           `json:"source_id"`
	URL            string          `json:"url"`
	Title          string          `json:"title"`
	Favorite       bool            `json:"favorite"`
	FavoriteAt     *int64          `json:"favorite_at"`
	ReaderFlags    int64           `json:"reader_flags"`
	ChapterFlags   int64           `json:"chapter_flags"`
	UpdateStrategy string          `json:"update_strategy"`
	Notes          string          `json:"notes"`
	Memo           json.RawMessage `json:"memo,omitempty"`
	SyncVersion    int64           `json:"sync_version"`
	Deleted        bool            `json:"deleted"`
}

type chapterDTO struct {
	MangaSourceID int64           `json:"manga_source_id"`
	MangaURL      string          `json:"manga_url"`
	URL           string          `json:"url"`
	Read          bool            `json:"read"`
	Bookmark      bool            `json:"bookmark"`
	LastPageRead  int64           `json:"last_page_read"`
	DateFetch     *int64          `json:"date_fetch"`
	DateUpload    *int64          `json:"date_upload"`
	Memo          json.RawMessage `json:"memo,omitempty"`
	SyncVersion   int64           `json:"sync_version"`
}

type categoryDTO struct {
	Name    string `json:"name"`
	Order   int64  `json:"order"`
	Flags   int64  `json:"flags"`
	Deleted bool   `json:"deleted"`
}

type mangaCategoryDTO struct {
	MangaSourceID int64  `json:"manga_source_id"`
	MangaURL      string `json:"manga_url"`
	Category      string `json:"category"`
	Deleted       bool   `json:"deleted"`
}

type historyDTO struct {
	MangaSourceID int64  `json:"manga_source_id"`
	MangaURL      string `json:"manga_url"`
	ChapterURL    string `json:"chapter_url"`
	ReadAt        *int64 `json:"read_at"`
	ReadDuration  int64  `json:"read_duration"`
}

type excludedScanlatorDTO struct {
	MangaSourceID int64  `json:"manga_source_id"`
	MangaURL      string `json:"manga_url"`
	Scanlator     string `json:"scanlator"`
	Deleted       bool   `json:"deleted"`
}

type preferenceDTO struct {
	Key     string          `json:"key"`
	Type    string          `json:"type"`
	Value   json.RawMessage `json:"value"`
	Deleted bool            `json:"deleted"`
}

type extensionStoreDTO struct {
	IndexURL         string `json:"index_url"`
	Name             string `json:"name"`
	BadgeLabel       string `json:"badge_label"`
	SigningKey       string `json:"signing_key"`
	ContactWebsite   string `json:"contact_website"`
	ContactDiscord   string `json:"contact_discord"`
	IsLegacy         bool   `json:"is_legacy"`
	ExtensionListURL string `json:"extension_list_url"`
	Deleted          bool   `json:"deleted"`
}

type changeSetDTO struct {
	Mangas             []mangaDTO             `json:"mangas,omitempty"`
	Chapters           []chapterDTO           `json:"chapters,omitempty"`
	Categories         []categoryDTO          `json:"categories,omitempty"`
	MangaCategories    []mangaCategoryDTO     `json:"manga_categories,omitempty"`
	History            []historyDTO           `json:"history,omitempty"`
	ExcludedScanlators []excludedScanlatorDTO `json:"excluded_scanlators,omitempty"`
	Preferences        []preferenceDTO        `json:"preferences,omitempty"`
	ExtensionStores    []extensionStoreDTO    `json:"extension_stores,omitempty"`
}

type pushRequest struct {
	DeviceID string `json:"device_id"`
	// Since is the caller's revision watermark; the response carries changes
	// from other devices in (since, newRev).
	Since   int64        `json:"since"`
	Changes changeSetDTO `json:"changes"`
}

type pushResponse struct {
	Rev     int64        `json:"rev"`
	Changes changeSetDTO `json:"changes"`
}

type pullResponse struct {
	Rev     int64        `json:"rev"`
	Changes changeSetDTO `json:"changes"`
}

type statusResponse struct {
	Rev                    int64 `json:"rev"`
	MangaCount             int64 `json:"manga_count"`
	ChapterCount           int64 `json:"chapter_count"`
	CategoryCount          int64 `json:"category_count"`
	HistoryCount           int64 `json:"history_count"`
	PreferenceCount        int64 `json:"preference_count"`
	ExtensionStoreCount    int64 `json:"extension_store_count"`
	ExcludedScanlatorCount int64 `json:"excluded_scanlator_count"`
	DeviceCount            int64 `json:"device_count"`
	AccountCreatedAt       int64 `json:"account_created_at"`
}

type serverInfoResponse struct {
	AllowRegistration bool     `json:"allow_registration"`
	Registration      string   `json:"registration"`
	Version           string   `json:"version"`
	Capabilities      []string `json:"capabilities"`
}

type registerRequest struct {
	Label      string `json:"label"`
	InviteCode string `json:"invite_code"`
}

type registerResponse struct {
	APIKey string `json:"api_key"`
	Label  string `json:"label"`
}

type errorResponse struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// capabilities is the set of features this server speaks, so a client can gate
// on it instead of guessing from the version string.
var capabilities = []string{
	"sync.v2",
	"entity.manga",
	"entity.chapter",
	"entity.category",
	"entity.manga_category",
	"entity.history",
	"entity.excluded_scanlator",
	"entity.preference",
	"entity.extension_store",
	"memo",
}

// nullIfZeroPtr renders a wire value as a pointer so JSON emits null instead of 0.
func zeroToNull(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

func nullToZero(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func dtoToStore(cs *changeSetDTO) *store.ChangeSet {
	out := &store.ChangeSet{}
	for _, m := range cs.Mangas {
		out.Mangas = append(out.Mangas, store.Manga{
			SourceID: m.SourceID, URL: m.URL, Title: m.Title,
			Favorite: m.Favorite, FavoriteAt: nullToZero(m.FavoriteAt),
			ReaderFlags: m.ReaderFlags, ChapterFlags: m.ChapterFlags,
			UpdateStrategy: m.UpdateStrategy, Notes: m.Notes, Memo: m.Memo,
			SyncVersion: m.SyncVersion, Deleted: m.Deleted,
		})
	}
	for _, c := range cs.Chapters {
		out.Chapters = append(out.Chapters, store.Chapter{
			MangaSourceID: c.MangaSourceID, MangaURL: c.MangaURL, URL: c.URL,
			Read: c.Read, Bookmark: c.Bookmark, LastPageRead: c.LastPageRead,
			DateFetch: nullToZero(c.DateFetch), DateUpload: nullToZero(c.DateUpload),
			Memo: c.Memo, SyncVersion: c.SyncVersion,
		})
	}
	for _, c := range cs.Categories {
		out.Categories = append(out.Categories, store.Category{
			Name: c.Name, Order: c.Order, Flags: c.Flags, Deleted: c.Deleted,
		})
	}
	for _, mc := range cs.MangaCategories {
		out.MangaCategories = append(out.MangaCategories, store.MangaCategory{
			MangaSourceID: mc.MangaSourceID, MangaURL: mc.MangaURL,
			Category: mc.Category, Deleted: mc.Deleted,
		})
	}
	for _, h := range cs.History {
		out.History = append(out.History, store.HistoryEntry{
			MangaSourceID: h.MangaSourceID, MangaURL: h.MangaURL, ChapterURL: h.ChapterURL,
			ReadAt: nullToZero(h.ReadAt), ReadDuration: h.ReadDuration,
		})
	}
	for _, es := range cs.ExcludedScanlators {
		out.ExcludedScanlators = append(out.ExcludedScanlators, store.ExcludedScanlator{
			MangaSourceID: es.MangaSourceID, MangaURL: es.MangaURL,
			Scanlator: es.Scanlator, Deleted: es.Deleted,
		})
	}
	for _, p := range cs.Preferences {
		out.Preferences = append(out.Preferences, store.Preference{
			Key: p.Key, Type: p.Type, Value: p.Value, Deleted: p.Deleted,
		})
	}
	for _, es := range cs.ExtensionStores {
		out.ExtensionStores = append(out.ExtensionStores, store.ExtensionStore{
			IndexURL: es.IndexURL, Name: es.Name, BadgeLabel: es.BadgeLabel,
			SigningKey: es.SigningKey, ContactWebsite: es.ContactWebsite,
			ContactDiscord: es.ContactDiscord, IsLegacy: es.IsLegacy,
			ExtensionListURL: es.ExtensionListURL, Deleted: es.Deleted,
		})
	}
	return out
}

func storeToDTO(cs *store.ChangeSet) changeSetDTO {
	out := changeSetDTO{}
	for _, m := range cs.Mangas {
		out.Mangas = append(out.Mangas, mangaDTO{
			SourceID: m.SourceID, URL: m.URL, Title: m.Title,
			Favorite: m.Favorite, FavoriteAt: zeroToNull(m.FavoriteAt),
			ReaderFlags: m.ReaderFlags, ChapterFlags: m.ChapterFlags,
			UpdateStrategy: m.UpdateStrategy, Notes: m.Notes, Memo: m.Memo,
			SyncVersion: m.SyncVersion, Deleted: m.Deleted,
		})
	}
	for _, c := range cs.Chapters {
		out.Chapters = append(out.Chapters, chapterDTO{
			MangaSourceID: c.MangaSourceID, MangaURL: c.MangaURL, URL: c.URL,
			Read: c.Read, Bookmark: c.Bookmark, LastPageRead: c.LastPageRead,
			DateFetch: zeroToNull(c.DateFetch), DateUpload: zeroToNull(c.DateUpload),
			Memo: c.Memo, SyncVersion: c.SyncVersion,
		})
	}
	for _, c := range cs.Categories {
		out.Categories = append(out.Categories, categoryDTO{
			Name: c.Name, Order: c.Order, Flags: c.Flags, Deleted: c.Deleted,
		})
	}
	for _, mc := range cs.MangaCategories {
		out.MangaCategories = append(out.MangaCategories, mangaCategoryDTO{
			MangaSourceID: mc.MangaSourceID, MangaURL: mc.MangaURL,
			Category: mc.Category, Deleted: mc.Deleted,
		})
	}
	for _, h := range cs.History {
		out.History = append(out.History, historyDTO{
			MangaSourceID: h.MangaSourceID, MangaURL: h.MangaURL, ChapterURL: h.ChapterURL,
			ReadAt: zeroToNull(h.ReadAt), ReadDuration: h.ReadDuration,
		})
	}
	for _, es := range cs.ExcludedScanlators {
		out.ExcludedScanlators = append(out.ExcludedScanlators, excludedScanlatorDTO{
			MangaSourceID: es.MangaSourceID, MangaURL: es.MangaURL,
			Scanlator: es.Scanlator, Deleted: es.Deleted,
		})
	}
	for _, p := range cs.Preferences {
		out.Preferences = append(out.Preferences, preferenceDTO{
			Key: p.Key, Type: p.Type, Value: p.Value, Deleted: p.Deleted,
		})
	}
	for _, es := range cs.ExtensionStores {
		out.ExtensionStores = append(out.ExtensionStores, extensionStoreDTO{
			IndexURL: es.IndexURL, Name: es.Name, BadgeLabel: es.BadgeLabel,
			SigningKey: es.SigningKey, ContactWebsite: es.ContactWebsite,
			ContactDiscord: es.ContactDiscord, IsLegacy: es.IsLegacy,
			ExtensionListURL: es.ExtensionListURL, Deleted: es.Deleted,
		})
	}
	return out
}
