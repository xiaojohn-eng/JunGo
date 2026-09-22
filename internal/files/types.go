// Package files exposes device-authenticated, contained, resumable file transfers.
package files

import (
	"context"
	"net/http"
	"os"
	"sync"
	"time"
)

const DefaultMaxChunkBytes int64 = 8 << 20
const stagingPrefix = ".meshlink-upload-"

type Share struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"-"`
	ReadOnly bool   `json:"readOnly"`
}

type Config struct {
	StateDir string
	Shares   []Share
	// Authorize must consult current revocation state and return a stable nonempty
	// device identity. Upload tasks are private to this identity.
	Authorize func(context.Context, *http.Request) (string, error)
	// Zero limits use the defaults: 1 TiB per file, 2 TiB reserved pending bytes,
	// 8 MiB per chunk, and 64 MiB free-space reserve.
	MaxUploadBytes  int64
	MaxPendingBytes int64
	MaxChunkBytes   int64
	MinFreeBytes    int64
}

type Entry struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Directory bool      `json:"directory"`
	Size      int64     `json:"size"`
	Modified  time.Time `json:"modified"`
	Version   string    `json:"version,omitempty"`
}

type BeginRequest struct {
	ShareID            string `json:"shareId"`
	Path               string `json:"path"`
	Size               int64  `json:"size"`
	SHA256             string `json:"sha256"`
	OverwriteConfirmed bool   `json:"overwriteConfirmed,omitempty"`
}

type Upload struct {
	ID      string `json:"id"`
	ShareID string `json:"shareId"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Offset  int64  `json:"offset"`
	State   string `json:"state"` // pending, publishing, completed, cancelled
	SHA256  string `json:"sha256"`
}

type diskUpload struct {
	Upload
	OwnerID            string `json:"ownerId"`
	OriginalPath       string `json:"originalPath"`
	OverwriteConfirmed bool   `json:"overwriteConfirmed"`
	StagePath          string `json:"stagePath,omitempty"`
	// PublicationVersion identifies our staging inode across rename/link. A
	// pre-existing file with the same bytes must not satisfy crash recovery.
	PublicationVersion string `json:"publicationVersion,omitempty"`
	RequestID          string `json:"requestId,omitempty"`
}

type uploadRequestKey struct{ owner, request string }

type uploadTask struct {
	mu sync.Mutex
	diskUpload
}

type openShare struct {
	Share
	root *os.Root
}

type Service struct {
	config    Config
	state     *os.Root
	lockFile  *os.File
	shares    map[string]*openShare
	mu        sync.Mutex // protects tasks and reserved bytes
	tasks     map[string]*uploadTask
	requests  map[uploadRequestKey]*uploadTask
	reserved  int64
	publishMu sync.Mutex
	closeOnce sync.Once
}
