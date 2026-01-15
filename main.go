package main

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"wgetter/database"
)

const (
	downloadsDir = "downloads"
	dataDir      = "data"
	dbPath       = "data/downloads.db"
	port         = 8000
)

var (
	templates       *template.Template
	activeDownloads = make(map[int64]*DownloadTask)
	mu              sync.Mutex
)

type DownloadTask struct {
	cancel context.CancelFunc
	ctx    context.Context
}

func main() {
	// Create data directory for database
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	// Initialize database
	if err := database.InitDB(dbPath); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer database.Close()

	// Create downloads directory
	if err := os.MkdirAll(downloadsDir, 0755); err != nil {
		log.Fatalf("Failed to create downloads directory: %v", err)
	}

	// Parse templates with custom functions
	funcMap := template.FuncMap{
		"humanize_bytes": humanizeBytes,
		"format_eta":     formatETA,
		"is_viewable":    isViewable,
		"truncate":       truncateString,
	}

	var err error
	templates, err = template.New("").Funcs(funcMap).ParseGlob("templates/*.html")
	if err != nil {
		log.Fatalf("Failed to parse templates: %v", err)
	}

	// Routes
	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/download", startDownloadHandler)
	http.HandleFunc("/downloads", listDownloadsHandler)
	http.HandleFunc("/download/", downloadActionHandler)
	http.HandleFunc("/file/", serveFileHandler)
	http.HandleFunc("/view/", viewFileHandler)

	log.Printf("Server starting on http://localhost:%d", port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", port), nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func humanizeBytes(size int64) string {
	if size == 0 {
		return ""
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	floatSize := float64(size)
	unitIndex := 0
	for floatSize >= 1024 && unitIndex < len(units)-1 {
		floatSize /= 1024
		unitIndex++
	}
	return fmt.Sprintf("%.2f %s", floatSize, units[unitIndex])
}

func formatETA(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	} else if seconds < 3600 {
		minutes := seconds / 60
		secs := seconds % 60
		return fmt.Sprintf("%dm %ds", minutes, secs)
	} else {
		hours := seconds / 3600
		minutes := (seconds % 3600) / 60
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
}

var viewableExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".svg": true,
	".webp": true, ".ico": true, ".bmp": true,
	".mp4": true, ".webm": true, ".ogv": true,
	".mp3": true, ".wav": true, ".ogg": true,
	".pdf": true, ".txt": true, ".html": true, ".htm": true,
	".css": true, ".js": true, ".json": true, ".xml": true,
}

func isViewable(filename string) bool {
	if filename == "" {
		return false
	}
	ext := strings.ToLower(filepath.Ext(filename))
	return viewableExtensions[ext]
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func extractFilename(urlStr string, headers http.Header) string {
	// Try Content-Disposition header first
	contentDisp := headers.Get("Content-Disposition")
	if contentDisp != "" {
		// Try quoted filename
		re := regexp.MustCompile(`filename\s*=\s*"([^"]+)"`)
		matches := re.FindStringSubmatch(contentDisp)
		if len(matches) > 1 {
			return matches[1]
		}

		// Try single-quoted filename
		re = regexp.MustCompile(`filename\s*=\s*'([^']+)'`)
		matches = re.FindStringSubmatch(contentDisp)
		if len(matches) > 1 {
			return matches[1]
		}

		// Try unquoted filename
		re = regexp.MustCompile(`filename\s*=\s*([^;]+)`)
		matches = re.FindStringSubmatch(contentDisp)
		if len(matches) > 1 {
			filename := strings.TrimSpace(matches[1])
			// Remove any surrounding quotes
			filename = strings.Trim(filename, `"'`)
			if filename != "" {
				return filename
			}
		}
	}

	// Fall back to URL path
	parsedURL, err := url.Parse(urlStr)
	if err == nil && parsedURL.Path != "" {
		parts := strings.Split(parsedURL.Path, "/")
		if len(parts) > 0 && parts[len(parts)-1] != "" {
			return parts[len(parts)-1]
		}
	}

	return "download"
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	downloads, err := database.GetDownloads()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"downloads": downloads,
	}

	if err := templates.ExecuteTemplate(w, "index.html", data); err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func startDownloadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	urlStr := r.FormValue("url")
	if urlStr == "" {
		http.Error(w, "URL is required", http.StatusBadRequest)
		return
	}

	downloadID, err := database.AddDownload(urlStr)
	if err != nil {
		log.Printf("ERROR: Failed to create download entry: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("NEW DOWNLOAD [#%d]: %s", downloadID, urlStr)

	// Start download in background
	go downloadFile(downloadID, urlStr, 0)

	// Return updated download list
	downloads, err := database.GetDownloads()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"downloads": downloads,
	}

	if err := templates.ExecuteTemplate(w, "download_list.html", data); err != nil {
		log.Printf("Template error: %v", err)
	}
}

func listDownloadsHandler(w http.ResponseWriter, r *http.Request) {
	downloads, err := database.GetDownloads()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"downloads": downloads,
	}

	if err := templates.ExecuteTemplate(w, "download_list.html", data); err != nil {
		log.Printf("Template error: %v", err)
	}
}

func downloadActionHandler(w http.ResponseWriter, r *http.Request) {
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 2 {
		http.NotFound(w, r)
		return
	}

	downloadID, err := strconv.ParseInt(pathParts[1], 10, 64)
	if err != nil {
		http.Error(w, "Invalid download ID", http.StatusBadRequest)
		return
	}

	action := ""
	if len(pathParts) >= 3 {
		action = pathParts[2]
	}

	switch {
	case action == "pause" && r.Method == http.MethodPost:
		pauseDownloadHandler(w, r, downloadID)
	case action == "resume" && r.Method == http.MethodPost:
		resumeDownloadHandler(w, r, downloadID)
	case action == "cancel" && r.Method == http.MethodPost:
		cancelDownloadHandler(w, r, downloadID)
	case action == "retry" && r.Method == http.MethodPost:
		retryDownloadHandler(w, r, downloadID)
	case action == "rename" && r.Method == http.MethodPut:
		renameDownloadHandler(w, r, downloadID)
	case action == "db-only" && r.Method == http.MethodDelete:
		deleteDownloadDBOnlyHandler(w, r, downloadID)
	case action == "" && r.Method == http.MethodDelete:
		deleteDownloadFullHandler(w, r, downloadID)
	default:
		http.NotFound(w, r)
	}
}

func pauseDownloadHandler(w http.ResponseWriter, r *http.Request, downloadID int64) {
	download, _ := database.GetDownload(downloadID)
	filename := "unknown"
	if download != nil && download.Filename != "" {
		filename = download.Filename
	}

	mu.Lock()
	if task, exists := activeDownloads[downloadID]; exists {
		task.cancel()
		delete(activeDownloads, downloadID)
		log.Printf("PAUSED [#%d]: %s", downloadID, filename)
	} else {
		log.Printf("PAUSE REQUESTED [#%d]: %s (not actively downloading)", downloadID, filename)
	}
	mu.Unlock()

	database.UpdateDownload(downloadID, map[string]interface{}{
		"status": database.StatusPaused,
	})

	renderDownloadList(w)
}

func resumeDownloadHandler(w http.ResponseWriter, r *http.Request, downloadID int64) {
	download, err := database.GetDownload(downloadID)
	if err != nil || download.Status != database.StatusPaused {
		renderDownloadList(w)
		return
	}

	resumeFrom := download.DownloadedBytes
	progress := 0
	if download.FileSize > 0 {
		progress = int((resumeFrom * 100) / download.FileSize)
	}

	log.Printf("RESUMING [#%d]: %s from %s (%d%%)",
		downloadID, download.Filename, humanizeBytes(resumeFrom), progress)

	go downloadFile(downloadID, download.URL, resumeFrom)
	renderDownloadList(w)
}

func cancelDownloadHandler(w http.ResponseWriter, r *http.Request, downloadID int64) {
	download, err := database.GetDownload(downloadID)
	if err != nil {
		renderDownloadList(w)
		return
	}

	filename := download.Filename
	if filename == "" {
		filename = "unknown"
	}

	// Stop active download
	mu.Lock()
	wasActive := false
	if task, exists := activeDownloads[downloadID]; exists {
		task.cancel()
		delete(activeDownloads, downloadID)
		wasActive = true
	}
	mu.Unlock()

	time.Sleep(100 * time.Millisecond) // Give it time to stop

	// Remove partial file
	if download.Filename != "" {
		filepath := path.Join(downloadsDir, download.Filename)
		if err := os.Remove(filepath); err == nil {
			log.Printf("CANCELLED [#%d]: %s - Partial file removed (%s downloaded)",
				downloadID, filename, humanizeBytes(download.DownloadedBytes))
		} else {
			log.Printf("CANCELLED [#%d]: %s", downloadID, filename)
		}
	} else {
		log.Printf("CANCELLED [#%d]: %s", downloadID, filename)
	}

	if !wasActive {
		log.Printf("   └─ Download was not actively running")
	}

	database.UpdateDownload(downloadID, map[string]interface{}{
		"status":           database.StatusCancelled,
		"downloaded_bytes": 0,
		"progress":         0,
		"speed":            0,
		"eta":              0,
	})

	renderDownloadList(w)
}

func retryDownloadHandler(w http.ResponseWriter, r *http.Request, downloadID int64) {
	download, err := database.GetDownload(downloadID)
	if err != nil {
		renderDownloadList(w)
		return
	}

	if download.Status != database.StatusFailed && download.Status != database.StatusCancelled {
		renderDownloadList(w)
		return
	}

	// Remove old partial file
	if download.Filename != "" {
		filepath := path.Join(downloadsDir, download.Filename)
		os.Remove(filepath)
	}

	filename := download.Filename
	if filename == "" {
		filename = "unknown"
	}
	statusStr := string(download.Status)

	log.Printf("RETRYING [#%d]: %s (previous status: %s)", downloadID, filename, statusStr)
	if download.Error != "" {
		log.Printf("   └─ Previous error: %s", download.Error)
	}

	// Reset and restart
	database.UpdateDownload(downloadID, map[string]interface{}{
		"status":           database.StatusPending,
		"progress":         0,
		"downloaded_bytes": 0,
		"error":            "",
	})

	go downloadFile(downloadID, download.URL, 0)
	renderDownloadList(w)
}

func renameDownloadHandler(w http.ResponseWriter, r *http.Request, downloadID int64) {
	newFilename := r.FormValue("new_filename")
	if newFilename == "" {
		renderDownloadList(w)
		return
	}

	download, err := database.GetDownload(downloadID)
	if err != nil || download.Filename == "" {
		renderDownloadList(w)
		return
	}

	oldPath := path.Join(downloadsDir, download.Filename)
	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		renderDownloadList(w)
		return
	}

	// Preserve extension if not provided
	oldExt := filepath.Ext(download.Filename)
	if filepath.Ext(newFilename) == "" {
		newFilename += oldExt
	}

	// Ensure unique filename
	newPath := path.Join(downloadsDir, newFilename)
	counter := 1
	baseNewFilename := newFilename
	for {
		if _, err := os.Stat(newPath); os.IsNotExist(err) || newPath == oldPath {
			break
		}
		ext := filepath.Ext(baseNewFilename)
		name := strings.TrimSuffix(baseNewFilename, ext)
		newFilename = fmt.Sprintf("%s_%d%s", name, counter, ext)
		newPath = path.Join(downloadsDir, newFilename)
		counter++
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		log.Printf("ERROR: Failed to rename [#%d]: %s -> %s: %v", downloadID, download.Filename, newFilename, err)
		renderDownloadList(w)
		return
	}

	log.Printf("RENAMED [#%d]: %s -> %s", downloadID, download.Filename, newFilename)

	database.UpdateDownload(downloadID, map[string]interface{}{
		"filename": newFilename,
	})

	renderDownloadList(w)
}

func deleteDownloadFullHandler(w http.ResponseWriter, r *http.Request, downloadID int64) {
	download, err := database.GetDownload(downloadID)
	filename := "unknown"
	fileSize := int64(0)

	if err == nil {
		filename = download.Filename
		fileSize = download.FileSize
		if download.Filename != "" {
			filepath := path.Join(downloadsDir, download.Filename)
			if err := os.Remove(filepath); err == nil {
				log.Printf("DELETED [#%d]: %s (%s) - File and DB entry removed",
					downloadID, filename, humanizeBytes(fileSize))
			} else {
				log.Printf("DELETED [#%d]: %s - DB entry removed (file not found)", downloadID, filename)
			}
		}
	} else {
		log.Printf("DELETED [#%d] - DB entry removed", downloadID)
	}

	database.DeleteDownload(downloadID)
	renderDownloadList(w)
}

func deleteDownloadDBOnlyHandler(w http.ResponseWriter, r *http.Request, downloadID int64) {
	download, _ := database.GetDownload(downloadID)
	filename := "unknown"
	if download != nil && download.Filename != "" {
		filename = download.Filename
	}

	log.Printf("DELETED FROM DB [#%d]: %s - File kept on disk", downloadID, filename)
	database.DeleteDownload(downloadID)
	renderDownloadList(w)
}

func serveFileHandler(w http.ResponseWriter, r *http.Request) {
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 2 {
		http.NotFound(w, r)
		return
	}

	downloadID, err := strconv.ParseInt(pathParts[1], 10, 64)
	if err != nil {
		http.Error(w, "Invalid download ID", http.StatusBadRequest)
		return
	}

	download, err := database.GetDownload(downloadID)
	if err != nil || download.Filename == "" {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	filePath := path.Join(downloadsDir, download.Filename)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		log.Printf("ERROR: Download requested [#%d]: %s - File not found", downloadID, download.Filename)
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	clientIP := r.RemoteAddr
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		clientIP = forwarded
	}

	log.Printf("DOWNLOAD TO CLIENT [#%d]: %s (%s) - Client: %s",
		downloadID, download.Filename, humanizeBytes(download.FileSize), clientIP)

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", download.Filename))
	http.ServeFile(w, r, filePath)
}

func viewFileHandler(w http.ResponseWriter, r *http.Request) {
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 2 {
		http.NotFound(w, r)
		return
	}

	downloadID, err := strconv.ParseInt(pathParts[1], 10, 64)
	if err != nil {
		http.Error(w, "Invalid download ID", http.StatusBadRequest)
		return
	}

	download, err := database.GetDownload(downloadID)
	if err != nil || download.Filename == "" {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	filePath := path.Join(downloadsDir, download.Filename)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		log.Printf("ERROR: View requested [#%d]: %s - File not found", downloadID, download.Filename)
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	clientIP := r.RemoteAddr
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		clientIP = forwarded
	}

	fileExt := filepath.Ext(download.Filename)
	log.Printf("VIEW REQUESTED [#%d]: %s (%s, %s) - Client: %s",
		downloadID, download.Filename, humanizeBytes(download.FileSize), fileExt, clientIP)

	http.ServeFile(w, r, filePath)
}

func renderDownloadList(w http.ResponseWriter) {
	downloads, err := database.GetDownloads()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"downloads": downloads,
	}

	if err := templates.ExecuteTemplate(w, "download_list.html", data); err != nil {
		log.Printf("Template error: %v", err)
	}
}

func downloadFile(downloadID int64, urlStr string, resumeFrom int64) {
	if resumeFrom > 0 {
		log.Printf("RESUME [#%d]: Resuming download from %s: %s", downloadID, humanizeBytes(resumeFrom), urlStr)
	} else {
		log.Printf("START [#%d]: Starting new download: %s", downloadID, urlStr)
	}

	// Create cancellable context
	ctx, cancel := context.WithCancel(context.Background())
	mu.Lock()
	activeDownloads[downloadID] = &DownloadTask{cancel: cancel, ctx: ctx}
	mu.Unlock()

	defer func() {
		mu.Lock()
		delete(activeDownloads, downloadID)
		mu.Unlock()
	}()

	database.UpdateDownload(downloadID, map[string]interface{}{
		"status": database.StatusDownloading,
	})

	// Get existing download info for resume
	download, err := database.GetDownload(downloadID)
	if err != nil {
		log.Printf("[#%d] Failed to get download info: %v", downloadID, err)
		database.UpdateDownload(downloadID, map[string]interface{}{
			"status": database.StatusFailed,
			"error":  err.Error(),
		})
		return
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		log.Printf("[#%d] Failed to create request: %v", downloadID, err)
		database.UpdateDownload(downloadID, map[string]interface{}{
			"status": database.StatusFailed,
			"error":  err.Error(),
		})
		return
	}

	// Set Range header for resume
	if resumeFrom > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeFrom))
	}

	// Execute request
	client := &http.Client{
		Timeout: 0, // No timeout for downloads
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return nil
		},
	}

	log.Printf("   [#%d] Connecting to server...", downloadID)
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("ERROR [#%d]: Connection failed: %v", downloadID, err)
		database.UpdateDownload(downloadID, map[string]interface{}{
			"status": database.StatusFailed,
			"error":  err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	statusMsg := "OK"
	if resp.StatusCode >= 400 {
		statusMsg = "ERROR"
	} else if resp.StatusCode == 206 {
		statusMsg = "PARTIAL"
	}
	log.Printf("   [#%d] %s - Connected - HTTP %d %s", downloadID, statusMsg, resp.StatusCode, http.StatusText(resp.StatusCode))

	// Determine filename
	var filename string
	var filepath string
	if download.Filename != "" {
		filename = download.Filename
		filepath = path.Join(downloadsDir, filename)
	} else {
		filename = extractFilename(urlStr, resp.Header)
		filepath = path.Join(downloadsDir, filename)

		// Ensure unique filename
		counter := 1
		baseFilename := filename
		for {
			if _, err := os.Stat(filepath); os.IsNotExist(err) {
				break
			}
			ext := path.Ext(baseFilename)
			name := strings.TrimSuffix(baseFilename, ext)
			filename = fmt.Sprintf("%s_%d%s", name, counter, ext)
			filepath = path.Join(downloadsDir, filename)
			counter++
		}
	}

	// Get file size
	var fileSize int64
	if resp.StatusCode == http.StatusPartialContent {
		contentRange := resp.Header.Get("Content-Range")
		if strings.Contains(contentRange, "/") {
			parts := strings.Split(contentRange, "/")
			if len(parts) > 1 {
				fileSize, _ = strconv.ParseInt(parts[1], 10, 64)
			}
		}
		if fileSize == 0 {
			fileSize = download.FileSize
		}
	} else {
		fileSize = resp.ContentLength
	}

	database.UpdateDownload(downloadID, map[string]interface{}{
		"filename":  filename,
		"file_size": fileSize,
	})

	sizeStr := humanizeBytes(fileSize)
	if fileSize == 0 {
		sizeStr = "Unknown size"
	}

	contentType := resp.Header.Get("Content-Type")
	log.Printf("   [#%d] File: %s", downloadID, filename)
	log.Printf("   [#%d] Size: %s", downloadID, sizeStr)
	if contentType != "" {
		log.Printf("   [#%d] Type: %s", downloadID, contentType)
	}

	// Open file for writing
	log.Printf("   [#%d] Opening file for writing...", downloadID)
	var file *os.File
	if resumeFrom > 0 {
		file, err = os.OpenFile(filepath, os.O_APPEND|os.O_WRONLY, 0644)
		log.Printf("   [#%d] Appending to existing file", downloadID)
	} else {
		file, err = os.Create(filepath)
		log.Printf("   [#%d] Creating new file", downloadID)
	}
	if err != nil {
		log.Printf("ERROR [#%d]: Failed to create file: %v", downloadID, err)
		database.UpdateDownload(downloadID, map[string]interface{}{
			"status": database.StatusFailed,
			"error":  err.Error(),
		})
		return
	}
	defer file.Close()

	log.Printf("DOWNLOADING [#%d]: %s", downloadID, filename)

	// Download loop
	downloaded := resumeFrom
	lastProgress := 0
	lastLogProgress := 0
	if fileSize > 0 {
		lastLogProgress = int((resumeFrom * 100) / fileSize)
	}
	startTime := time.Now()
	lastUpdateTime := startTime

	buffer := make([]byte, 8192)
	for {
		select {
		case <-ctx.Done():
			// Cancelled or paused
			progress := 0
			if fileSize > 0 {
				progress = int((downloaded * 100) / fileSize)
			}
			database.UpdateDownload(downloadID, map[string]interface{}{
				"downloaded_bytes": downloaded,
				"speed":            0,
				"eta":              0,
			})
			log.Printf("STOPPED [#%d]: Download stopped - %s downloaded (%d%%)",
				downloadID, humanizeBytes(downloaded), progress)
			return
		default:
			n, err := resp.Body.Read(buffer)
			if n > 0 {
				_, writeErr := file.Write(buffer[:n])
				if writeErr != nil {
					log.Printf("[#%d] Failed to write file: %v", downloadID, writeErr)
					database.UpdateDownload(downloadID, map[string]interface{}{
						"status": database.StatusFailed,
						"error":  writeErr.Error(),
					})
					return
				}
				downloaded += int64(n)

				if fileSize > 0 {
					progress := int((downloaded * 100) / fileSize)
					currentTime := time.Now()

					// Update every 5% or every second
					if (progress != lastProgress && progress%5 == 0) ||
						currentTime.Sub(lastUpdateTime) >= time.Second {

						elapsed := currentTime.Sub(startTime).Seconds()
						var speed int64
						if elapsed > 0 {
							speed = int64(float64(downloaded-resumeFrom) / elapsed)
						}
						remaining := fileSize - downloaded
						var eta int
						if speed > 0 {
							eta = int(remaining / speed)
						}

						database.UpdateDownload(downloadID, map[string]interface{}{
							"progress":         progress,
							"downloaded_bytes": downloaded,
							"speed":            speed,
							"eta":              eta,
						})
						lastProgress = progress
						lastUpdateTime = currentTime
					}

					// Log every 10%
					if progress >= lastLogProgress+10 {
						elapsed := currentTime.Sub(startTime).Seconds()
						var speed int64
						if elapsed > 0 {
							speed = int64(float64(downloaded-resumeFrom) / elapsed)
						}
						var eta int
						if speed > 0 {
							eta = int((fileSize - downloaded) / speed)
						}
						log.Printf("PROGRESS [#%d]: %d%% (%s / %s) - %s/s - ETA: %s",
							downloadID, progress,
							humanizeBytes(downloaded), humanizeBytes(fileSize),
							humanizeBytes(speed), formatETA(eta))
						lastLogProgress = progress
					}
				}
			}

			if err == io.EOF {
				// Download complete
				elapsed := time.Since(startTime)
				avgSpeed := int64(0)
				if elapsed.Seconds() > 0 {
					avgSpeed = int64(float64(downloaded-resumeFrom) / elapsed.Seconds())
				}

				database.UpdateDownload(downloadID, map[string]interface{}{
					"status":           database.StatusCompleted,
					"progress":         100,
					"downloaded_bytes": downloaded,
				})

				log.Printf("COMPLETED [#%d]: Download completed successfully!", downloadID)
				log.Printf("   File: %s", filename)
				log.Printf("   Size: %s", humanizeBytes(fileSize))
				log.Printf("   Time: %s", elapsed.Round(time.Second))
				log.Printf("   Avg Speed: %s/s", humanizeBytes(avgSpeed))
				return
			}

			if err != nil {
				log.Printf("ERROR [#%d]: Download failed: %v", downloadID, err)
				database.UpdateDownload(downloadID, map[string]interface{}{
					"status": database.StatusFailed,
					"error":  err.Error(),
				})
				return
			}
		}
	}
}
