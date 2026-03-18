package main

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"
)

const defaultSTTURL = "http://10.0.1.226:8300"

func sttURL() string {
	if u := os.Getenv("SWARM_STT_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return defaultSTTURL
}

// handleSwarmTranscribeAPI handles POST /api/swarm/transcribe.
// Accepts multipart form with a 'file' field (any audio format supported by Whisper),
// forwards to the speaches STT service via streaming pipe, and returns the transcript as JSON.
func handleSwarmTranscribeAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Enforce hard upload size limit before parsing (25 MB ~= ~15 min audio at 224kbps)
	r.Body = http.MaxBytesReader(w, r.Body, 25<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "parse error: " + err.Error()}) //nolint:errcheck
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "file field missing"}) //nolint:errcheck
		return
	}
	defer file.Close()

	// Use a pipe to stream audio directly to the upstream request without buffering in RAM.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		defer pw.Close()

		fw, err := mw.CreateFormFile("file", sanitizeFilename(header.Filename))
		if err != nil {
			pw.CloseWithError(fmt.Errorf("multipart build: %w", err))
			return
		}
		if _, err = io.Copy(fw, file); err != nil {
			pw.CloseWithError(fmt.Errorf("audio copy: %w", err))
			return
		}

		model := r.FormValue("model")
		if model == "" {
			model = "Systran/faster-distil-whisper-large-v3"
		}
		if err := mw.WriteField("model", model); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := mw.WriteField("response_format", "json"); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := mw.Close(); err != nil {
			pw.CloseWithError(err)
			return
		}
	}()

	target := fmt.Sprintf("%s/v1/audio/transcriptions", sttURL())
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, pr)
	if err != nil {
		pr.CloseWithError(err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "request build error"}) //nolint:errcheck
		return
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": "STT unreachable: " + err.Error()}) //nolint:errcheck
		return
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": "STT read error: " + readErr.Error()}) //nolint:errcheck
		return
	}

	if resp.StatusCode != http.StatusOK {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
			"error":  "STT returned " + resp.Status,
			"detail": string(body),
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(body) //nolint:errcheck
}

// sanitizeFilename returns a safe filename for upstream logging — only the base name,
// no path components. Trusting user-supplied filenames can cause log injection.
func sanitizeFilename(name string) string {
	// Strip any path separators the client might supply
	name = strings.TrimSpace(name)
	for _, sep := range []string{"/", "\\"} {
		if i := strings.LastIndex(name, sep); i >= 0 {
			name = name[i+1:]
		}
	}
	if name == "" {
		return "recording"
	}
	return name
}
