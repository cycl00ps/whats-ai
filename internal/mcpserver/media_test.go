package mcpserver

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestUploadExpiryAndQuota(t *testing.T) {
	s := &Server{uploads: &uploads{files: make(map[string]upload)}}
	defer s.CloseUploads()
	uploadFile := func(text string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, err := form.CreateFormFile("file", "test.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/media/uploads", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		res := httptest.NewRecorder()
		s.uploadMedia(res, req)
		return res
	}
	s.uploads.bytes = stagingLimit - 1
	if res := uploadFile("too large"); res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("quota response = %d", res.Code)
	}
	s.uploads.bytes = 0
	res := uploadFile("hello")
	if res.Code != http.StatusCreated {
		t.Fatalf("upload response = %d %s", res.Code, res.Body.String())
	}
	var response map[string]string
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	id := response["upload_id"]
	file := s.uploads.files[id]
	if data, err := os.ReadFile(file.path); err != nil || string(data) != "hello" {
		t.Fatalf("staged content = %q, %v", data, err)
	}
	s.uploads.prune(time.Now().Add(uploadLifetime))
	if _, err := os.Stat(file.path); !os.IsNotExist(err) || len(s.uploads.files) != 0 || s.uploads.bytes != 0 {
		t.Fatalf("expiry did not remove staged upload: %v", err)
	}
}
