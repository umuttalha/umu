package api

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/umuttalha/umut/internal/state"
	"github.com/umuttalha/umut/internal/storage"
)

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}

	if err := r.ParseMultipartForm(256 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "parse multipart form: "+err.Error())
		return
	}

	file, header, err := r.FormFile("source")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing 'source' file field: "+err.Error())
		return
	}
	defer file.Close()

	dataDir := os.Getenv("UMUT_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/umut"
	}
	uploadDir := filepath.Join(dataDir, "uploads", name)
	if err := os.RemoveAll(uploadDir); err != nil {
		writeError(w, http.StatusInternalServerError, "clean upload dir: "+err.Error())
		return
	}
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "create upload dir: "+err.Error())
		return
	}

	filename := header.Filename
	ext := filepath.Ext(filename)
	switch ext {
	case ".zip":
		if err := extractZip(file, uploadDir, header.Size); err != nil {
			os.RemoveAll(uploadDir)
			writeError(w, http.StatusBadRequest, "extract zip: "+err.Error())
			return
		}
	case ".gz":
		writeError(w, http.StatusBadRequest, "tar.gz not yet supported, use .zip")
		return
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported format %q — use .zip", ext))
		return
	}

	absDir, _ := filepath.Abs(uploadDir)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":    "uploaded",
		"project":   name,
		"build_dir": absDir,
		"size":      header.Size,
	})
}

func (s *Server) handleSourceUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}

	if err := r.ParseMultipartForm(256 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "parse multipart form: "+err.Error())
		return
	}

	name := r.FormValue("project")
	if name == "" {
		name = r.URL.Query().Get("project")
	}
	if name == "" {
		writeError(w, http.StatusBadRequest, "project name required")
		return
	}

	file, header, err := r.FormFile("source")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing 'source' file field: "+err.Error())
		return
	}
	defer file.Close()

	dataDir := os.Getenv("UMUT_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/umut"
	}
	uploadDir := filepath.Join(dataDir, "uploads", name)
	if err := os.RemoveAll(uploadDir); err != nil {
		writeError(w, http.StatusInternalServerError, "clean upload dir: "+err.Error())
		return
	}
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "create upload dir: "+err.Error())
		return
	}

	ext := filepath.Ext(header.Filename)
	switch ext {
	case ".zip":
		if err := extractZip(file, uploadDir, header.Size); err != nil {
			os.RemoveAll(uploadDir)
			writeError(w, http.StatusBadRequest, "extract zip: "+err.Error())
			return
		}
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported format %q — use .zip", ext))
		return
	}

	absDir, _ := filepath.Abs(uploadDir)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":    "uploaded",
		"project":   name,
		"build_dir": absDir,
	})
}

func (s *Server) handleInjectSource(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}

	project, exists := s.store.Get(name)
	if !exists {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	if err := r.ParseMultipartForm(256 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "parse multipart form: "+err.Error())
		return
	}

	serviceName := r.FormValue("service")
	if serviceName == "" {
		serviceName = r.URL.Query().Get("service")
	}
	if serviceName == "" {
		serviceName = "main"
	}

	file, header, err := r.FormFile("source")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing 'source' file field: "+err.Error())
		return
	}
	defer file.Close()

	dataDir := os.Getenv("UMUT_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/umut"
	}
	uploadDir := filepath.Join(dataDir, "uploads", name)
	os.RemoveAll(uploadDir)
	os.MkdirAll(uploadDir, 0755)

	ext := filepath.Ext(header.Filename)
	switch ext {
	case ".zip":
		if err := extractZip(file, uploadDir, header.Size); err != nil {
			os.RemoveAll(uploadDir)
			writeError(w, http.StatusBadRequest, "extract zip: "+err.Error())
			return
		}
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported format %q — use .zip", ext))
		return
	}

	var targetSvc *state.Service
	for _, svc := range project.Services {
		if svc.Name == serviceName {
			targetSvc = svc
			break
		}
	}
	if targetSvc == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("service %q not found", serviceName))
		return
	}

	targetDisk := targetSvc.DiskPath
	if targetSvc.UserDataDisk != "" {
		targetDisk = targetSvc.UserDataDisk
	}

	if err := storage.InjectSourceIntoDisk(targetDisk, uploadDir); err != nil {
		writeError(w, http.StatusInternalServerError, "inject source: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "injected",
		"project":  name,
		"service":  serviceName,
		"disk":     targetDisk,
	})
}

func (s *Server) handleListUploads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "use GET")
		return
	}

	dataDir := os.Getenv("UMUT_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/umut"
	}
	uploadDir := filepath.Join(dataDir, "uploads")

	entries, err := os.ReadDir(uploadDir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, []interface{}{})
			return
		}
		writeError(w, http.StatusInternalServerError, "read upload dir: "+err.Error())
		return
	}

	type UploadInfo struct {
		Project   string `json:"project"`
		BuildDir  string `json:"build_dir"`
		UpdatedAt string `json:"updated_at,omitempty"`
	}

	var uploads []UploadInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		abs, _ := filepath.Abs(filepath.Join(uploadDir, e.Name()))
		uploads = append(uploads, UploadInfo{
			Project:   e.Name(),
			BuildDir:  abs,
			UpdatedAt: info.ModTime().UTC().Format(time.RFC3339),
		})
	}

	writeJSON(w, http.StatusOK, uploads)
}

func extractZip(src io.Reader, destDir string, _ int64) error {
	tmpFile, err := os.CreateTemp("", "umut-upload-*.zip")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := io.Copy(tmpFile, src); err != nil {
		tmpFile.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	tmpFile.Close()

	zipReader, err := zip.OpenReader(tmpFile.Name())
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer zipReader.Close()

	for _, f := range zipReader.File {
		destPath := filepath.Join(destDir, f.Name)
		if !checkPathSafe(destPath, destDir) {
			continue
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(destPath, f.Mode())
			continue
		}

		os.MkdirAll(filepath.Dir(destPath), 0755)

		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open zip entry %s: %w", f.Name, err)
		}

		out, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return fmt.Errorf("create file %s: %w", destPath, err)
		}

		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return fmt.Errorf("extract file %s: %w", f.Name, err)
		}
		out.Close()
		rc.Close()
	}

	return nil
}

func checkPathSafe(destPath, destDir string) bool {
	clean := filepath.Clean(destPath)
	return clean == destDir || len(clean) > len(destDir) && (clean[:len(destDir)] == destDir || clean[:len(destDir)+1] == destDir+"/" || clean[:len(destDir)+1] == destDir+string(filepath.Separator))
}
