package app

import (
	"context"
	"errors"
	"net/http"
	"time"
	"vdome/internal/domain"
	"vdome/internal/media"
)

func (h *HTTP) startStream(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	id, err := h.m.StartStream(ctx, token(r), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, domain.ErrUnauthorized) {
			apiError(w, err)
			return
		}
		message := "Не удалось подключить видеопоток. Можно запросить запасной кадр вручную."
		var failure *media.StartError
		if errors.As(err, &failure) {
			message = failure.Error()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			message = "Камера не ответила за 25 секунд. Можно запросить кадр вручную."
		}
		writeJSON(w, 503, map[string]string{"error": message})
		return
	}
	writeJSON(w, 200, map[string]string{"id": id, "url": "/api/streams/" + id + "/index.m3u8"})
}
func (h *HTTP) streamFile(w http.ResponseWriter, r *http.Request) {
	b, mime, err := h.m.StreamFile(token(r), r.PathValue("stream"), r.PathValue("file"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	_, _ = w.Write(b)
}
func (h *HTTP) stopStream(w http.ResponseWriter, r *http.Request) {
	if err := h.m.StopStream(token(r), r.PathValue("stream")); err != nil {
		apiError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
