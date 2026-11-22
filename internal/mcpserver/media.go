package mcpserver

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"

	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/goodtune/whatsapp-mcp/internal/wa"
)

const uploadLimit = 32 << 20
const stagingLimit = 128 << 20
const uploadLifetime = 15 * time.Minute

type upload struct {
	path    string
	size    int64
	expires time.Time
}

type uploads struct {
	mu    sync.Mutex
	files map[string]upload
	bytes int64
}

func (s *Server) RegisterMedia(mux *http.ServeMux) {
	mux.HandleFunc("POST /media/uploads", s.uploadMedia)
	mux.HandleFunc("GET /media/attachments", s.serveAttachment)
}

func (s *Server) Maintain() {
	s.uploads.mu.Lock()
	s.uploads.prune(time.Now())
	s.uploads.mu.Unlock()
}

// CloseUploads releases temporary files on worker shutdown.
func (s *Server) CloseUploads() {
	s.uploads.mu.Lock()
	defer s.uploads.mu.Unlock()
	s.uploads.prune(time.Now().Add(uploadLifetime))
}

func (u *uploads) prune(now time.Time) {
	for id, file := range u.files {
		if now.Before(file.expires) {
			continue
		}
		if err := os.RemoveAll(filepath.Dir(file.path)); err == nil {
			delete(u.files, id)
			u.bytes -= file.size
		}
	}
}

func (s *Server) uploadMedia(w http.ResponseWriter, r *http.Request) {
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(time.Minute))
	defer controller.SetReadDeadline(time.Time{})
	r.Body = http.MaxBytesReader(w, r.Body, uploadLimit+(1<<16))
	reader, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "expected multipart file field", 400)
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() == "" {
		http.Error(w, "expected multipart file field", 400)
		return
	}
	u := s.uploads
	u.mu.Lock()
	defer u.mu.Unlock()
	u.prune(time.Now())
	available := min(int64(uploadLimit), stagingLimit-u.bytes)
	if available <= 0 {
		http.Error(w, "upload staging is full", 507)
		return
	}
	dir, err := os.MkdirTemp("", "whatsapp-upload-")
	if err != nil {
		http.Error(w, "cannot stage upload", 500)
		return
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	name := filepath.Base(strings.ReplaceAll(part.FileName(), "\\", "/"))
	if name == "." || name == ".." || name == "/" {
		http.Error(w, "invalid filename", 400)
		return
	}
	path := filepath.Join(dir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		http.Error(w, "cannot stage upload", 500)
		return
	}
	n, err := io.Copy(file, io.LimitReader(part, available+1))
	closeErr := file.Close()
	if n > available {
		http.Error(w, "upload exceeds available size limit", 413)
		return
	}
	if err != nil || closeErr != nil {
		http.Error(w, "cannot read upload", 400)
		return
	}
	if _, err := reader.NextPart(); err != io.EOF {
		http.Error(w, "expected exactly one file", 400)
		return
	}
	var secret [16]byte
	_, _ = rand.Read(secret[:])
	id := hex.EncodeToString(secret[:])
	u.files[id] = upload{path: path, size: n, expires: time.Now().Add(uploadLifetime)}
	u.bytes += n
	keep = true
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"upload_id": id})
}

func attachmentPath(jid, id string) string {
	return "/media/attachments?" + url.Values{"chat_jid": {jid}, "message_id": {id}}.Encode()
}

func (s *Server) serveAttachment(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filename, data, err := s.wa.FetchAttachment(r.Context(), q.Get("message_id"), q.Get("chat_jid"))
	if err != nil {
		if errors.Is(err, wa.ErrAttachmentUnavailable) {
			http.NotFound(w, r)
		} else {
			http.Error(w, "WhatsApp attachment unavailable", 502)
		}
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(filename)}))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	_, _ = w.Write(data)
}
