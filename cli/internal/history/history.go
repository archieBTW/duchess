package history

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const fileName = "executions.jsonl"

// Entry is a persisted execution audit log record.
type Entry struct {
	Timestamp  time.Time `json:"timestamp"`
	Title      string    `json:"title"`
	Kind       string    `json:"kind"` // playbook | script | ssh | ops | cp
	Target     string    `json:"target"`
	Engine     string    `json:"engine"`
	Venv       string    `json:"venv,omitempty"`
	ExitCode   int       `json:"exitCode"`
	DurationMs int64     `json:"durationMs"`
	Stdout     string    `json:"stdout,omitempty"`
	Stderr     string    `json:"stderr,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// Succeeded reports whether the execution exited cleanly with no error.
func (e Entry) Succeeded() bool {
	return e.ExitCode == 0 && e.Error == ""
}

// Append persists a single execution entry as a JSON line in dir/executions.jsonl.
func Append(dir string, entry Entry) error {
	if dir == "" {
		return nil
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, fileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	return err
}

// LoadRecent returns up to limit execution entries, ordered newest first.
func LoadRecent(dir string, limit int) ([]Entry, error) {
	if dir == "" {
		return nil, nil
	}
	f, err := os.Open(filepath.Join(dir, fileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var all []Entry
	scanner := bufio.NewScanner(f)
	// Support large playbook outputs up to 4 MiB per line
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err == nil {
			all = append(all, e)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Reverse so newest is first
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}
