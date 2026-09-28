// Package logjson turns the stdlib log stream of a process into structured
// JSON lines with a process-role field, so multi-process chaos runs can be
// filtered and correlated without touching the database.
package logjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Writer is an io.Writer that emits one JSON object per input line:
// {"time": "...", "role": "...", "msg": "..."}. Point log.SetOutput (and
// gin.DefaultWriter) at one per process.
type Writer struct {
	Role string

	mu  sync.Mutex
	buf []byte
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf = append(w.buf, p...)

	for {
		idx := bytes.IndexByte(w.buf, '\n')
		if idx < 0 {
			break
		}

		line := strings.TrimRight(string(w.buf[:idx]), "\r")
		w.buf = append(w.buf[:0], w.buf[idx+1:]...)

		if strings.TrimSpace(line) == "" {
			continue
		}

		w.emit(line)
	}

	return len(p), nil
}

func (w *Writer) emit(msg string) {
	entry := map[string]string{
		"time": time.Now().UTC().Format(time.RFC3339Nano),
		"role": w.Role,
		"msg":  msg,
	}

	encoded, err := json.Marshal(entry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s %s %s\n", time.Now().UTC().Format(time.RFC3339), w.Role, msg)
		return
	}

	fmt.Fprintln(os.Stderr, string(encoded))
}
