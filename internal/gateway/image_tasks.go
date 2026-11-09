package gateway

import (
	"context"
	"net/http"
	"sync"
	"time"
)

const (
	codarImageTaskPath = "/v1/images/tasks/"
	codarImageTaskTTL  = 24 * time.Hour
	maxCodarImageTasks = 4096
)

type codarImageTask struct {
	id, owner, model         string
	created, finished        time.Time
	state, imageURL, message string
	errorStatus              int
}

type codarImageTasks struct {
	mu      sync.Mutex
	entries map[string]codarImageTask
	workers sync.WaitGroup
	closing bool
}

func newCodarImageTasks() *codarImageTasks {
	return &codarImageTasks{entries: make(map[string]codarImageTask)}
}

// Reserve an ID and space before calling the billable upstream operation.
func (s *codarImageTasks) reserve(owner, model string, now time.Time) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return "", false
	}
	if len(s.entries) >= maxCodarImageTasks {
		var oldest codarImageTask
		for id, task := range s.entries {
			if task.finished.IsZero() {
				continue
			}
			if now.Sub(task.finished) >= codarImageTaskTTL {
				delete(s.entries, id)
			} else if oldest.finished.IsZero() || task.finished.Before(oldest.finished) {
				oldest = task
			}
		}
		if len(s.entries) >= maxCodarImageTasks {
			if oldest.id == "" {
				return "", false
			}
			delete(s.entries, oldest.id)
		}
	}
	id := random(16)
	s.entries[id] = codarImageTask{id: id, owner: owner, model: model, created: now, state: "queued"}
	return id, true
}

func (s *codarImageTasks) remove(id string) {
	s.mu.Lock()
	delete(s.entries, id)
	s.mu.Unlock()
}

func (s *codarImageTasks) lookup(id, owner string) (codarImageTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, found := s.entries[id]
	if !found || t.owner != owner {
		return codarImageTask{}, false
	}
	if !t.finished.IsZero() && time.Since(t.finished) >= codarImageTaskTTL {
		delete(s.entries, id)
		return codarImageTask{}, false
	}
	return t, true
}

func (s *codarImageTasks) start(run func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return false
	}
	s.workers.Go(run)
	return true
}

func (s *codarImageTasks) update(id string, state string, finish func(*codarImageTask)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.entries[id]
	t.state = state
	if finish != nil {
		finish(&t)
		t.finished = time.Now()
	}
	s.entries[id] = t
}

func (s *codarImageTasks) close() {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	s.workers.Wait()
}

func (a *App) runImageTask(taskID, upstreamID string, start time.Time, account M, route Route, row M, keyID string) {
	a.imageTasks.update(taskID, "running", nil)
	ctx, cancel := context.WithDeadline(a.ctx, start.Add(codarImageDeadline))
	defer cancel()
	imageURL, err := a.awaitCodarImage(ctx, account, &route, upstreamID)
	if err != nil {
		status, msg := codarImageWaitError(err)
		row["ok"], row["status"], row["error"] = false, status, msg
		a.recordImageRequest(row, keyID, start, nil)
		a.imageTasks.update(taskID, "failed", func(t *codarImageTask) { t.errorStatus, t.message = status, msg })
		return
	}
	a.imageSucceeded(row)
	a.recordImageRequest(row, keyID, start, nil)
	a.imageTasks.update(taskID, "succeeded", func(t *codarImageTask) { t.imageURL = imageURL })
}

func (a *App) serveImageTask(w http.ResponseWriter, r *http.Request, id string) {
	key, err := a.auth.API(r, "")
	if err != nil {
		apiError(w, 401, err.Error())
		return
	}
	task, ok := a.imageTasks.lookup(id, text(key["id"]))
	if !ok {
		apiError(w, 404, "生图任务不存在或已过期")
		return
	}
	if _, err = a.auth.API(r, task.model); err != nil {
		apiError(w, 403, err.Error())
		return
	}
	out := M{"task_id": id, "status": task.state, "created": task.created.Unix()}
	switch task.state {
	case "succeeded":
		if err = a.writeCodarImage(w, r, out, task.imageURL); err != nil {
			codarImageError(w, statusOf(err), err.Error(), id)
		}
		return
	case "failed":
		out["error"] = M{"message": task.message, "type": "upstream_error", "status": task.errorStatus}
	default:
		w.Header().Set("Retry-After", "2")
	}
	jsonResponse(w, 200, out)
}
