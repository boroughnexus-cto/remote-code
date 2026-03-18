package main

import (
	"bytes"
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
// forwards to the speaches STT service, and returns the transcript as JSON.
func handleSwarmTranscribeAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
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

	// Build multipart request to speaches
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	fw, err := mw.CreateFormFile("file", header.Filename)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "multipart build error"}) //nolint:errcheck
		return
	}
	if _, err = io.Copy(fw, file); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "audio copy error"}) //nolint:errcheck
		return
	}

	model := r.FormValue("model")
	if model == "" {
		model = "Systran/faster-distil-whisper-large-v3"
	}
	mw.WriteField("model", model)         //nolint:errcheck
	mw.WriteField("language", "en")       //nolint:errcheck
	mw.WriteField("response_format", "json") //nolint:errcheck
	mw.Close()

	target := fmt.Sprintf("%s/v1/audio/transcriptions", sttURL())
	req, err := http.NewRequest(http.MethodPost, target, &buf)
	if err != nil {
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

	body, _ := io.ReadAll(resp.Body)

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
