// Package discovery scans ~/.claude/projects/ for Claude Code session
// JSONL files, providing filesystem-backed session metadata.
package discovery

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// SessionInfo holds metadata extracted from a session JSONL file.
type SessionInfo struct {
	UUID       string    // session UUID (filename stem)
	ProjectDir string    // encoded project directory name
	WorkDir    string    // working directory (from JSONL cwd field)
	GitBranch  string    // git branch (from JSONL gitBranch field)
	ModTime    time.Time // last modification time of the JSONL file
	Active     bool      // true if a claude process has this session's JSONL open
}

type cacheEntry struct {
	info    SessionInfo
	fetched time.Time
}

// Scanner walks a Claude Code projects directory for session files.
type Scanner struct {
	baseDir  string
	cacheTTL time.Duration

	mu    sync.Mutex
	cache map[string]cacheEntry // keyed by UUID

	activeMu      sync.Mutex
	activeCache   map[string]bool
	activeFetched time.Time
}

// NewScanner creates a Scanner rooted at baseDir (typically ~/.claude/projects).
func NewScanner(baseDir string) *Scanner {
	return &Scanner{
		baseDir:  baseDir,
		cacheTTL: 60 * time.Second,
		cache:    make(map[string]cacheEntry),
	}
}

// Scan returns all sessions whose JSONL files were modified within maxAge.
// If maxAge is 0, all sessions are returned.
func (s *Scanner) Scan(maxAge time.Duration) ([]SessionInfo, error) {
	cutoff := time.Time{}
	if maxAge > 0 {
		cutoff = time.Now().Add(-maxAge)
	}

	projDirs, err := os.ReadDir(s.baseDir)
	if err != nil {
		return nil, err
	}

	active := s.activeUUIDs()

	var results []SessionInfo
	for _, pd := range projDirs {
		if !pd.IsDir() {
			continue
		}
		// Skip memory directories.
		if pd.Name() == "memory" {
			continue
		}
		projPath := filepath.Join(s.baseDir, pd.Name())
		entries, err := os.ReadDir(projPath)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			uuid := strings.TrimSuffix(e.Name(), ".jsonl")
			if !IsUUID(uuid) {
				continue
			}

			fi, err := e.Info()
			if err != nil {
				continue
			}
			if !cutoff.IsZero() && fi.ModTime().Before(cutoff) {
				continue
			}

			info, err := s.getOrFetch(uuid, pd.Name(), projPath, fi.ModTime())
			if err != nil {
				continue
			}
			info.Active = active[uuid]
			results = append(results, info)
		}
	}
	return results, nil
}

// Get looks up a single session by UUID across all project directories.
func (s *Scanner) Get(uuid string) (*SessionInfo, error) {
	// Check cache first.
	s.mu.Lock()
	if ce, ok := s.cache[uuid]; ok && time.Since(ce.fetched) < s.cacheTTL {
		s.mu.Unlock()
		info := ce.info
		return &info, nil
	}
	s.mu.Unlock()

	// Walk project dirs to find the JSONL file.
	projDirs, err := os.ReadDir(s.baseDir)
	if err != nil {
		return nil, err
	}
	for _, pd := range projDirs {
		if !pd.IsDir() || pd.Name() == "memory" {
			continue
		}
		jsonlPath := filepath.Join(s.baseDir, pd.Name(), uuid+".jsonl")
		fi, err := os.Stat(jsonlPath)
		if err != nil {
			continue
		}
		info, err := s.getOrFetch(uuid, pd.Name(), filepath.Join(s.baseDir, pd.Name()), fi.ModTime())
		if err != nil {
			return nil, err
		}
		return &info, nil
	}
	return nil, nil
}

func (s *Scanner) getOrFetch(uuid, projDir, projPath string, modTime time.Time) (SessionInfo, error) {
	s.mu.Lock()
	if ce, ok := s.cache[uuid]; ok && time.Since(ce.fetched) < s.cacheTTL {
		// Update modtime in case it changed.
		ce.info.ModTime = modTime
		s.mu.Unlock()
		return ce.info, nil
	}
	s.mu.Unlock()

	info := SessionInfo{
		UUID:       uuid,
		ProjectDir: projDir,
		ModTime:    modTime,
	}

	// Read first ~20 lines to extract cwd and gitBranch.
	jsonlPath := filepath.Join(projPath, uuid+".jsonl")
	if err := extractMetadata(jsonlPath, &info); err != nil {
		// Still return what we have — UUID and modtime are useful.
		info.WorkDir = decodeProjectDir(projDir)
	}

	s.mu.Lock()
	s.cache[uuid] = cacheEntry{info: info, fetched: time.Now()}
	s.mu.Unlock()

	return info, nil
}

// extractMetadata reads the first lines of a JSONL file to extract
// cwd and gitBranch fields.
func extractMetadata(path string, info *SessionInfo) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for i := 0; i < 20 && scanner.Scan(); i++ {
		var line struct {
			CWD       string `json:"cwd"`
			GitBranch string `json:"gitBranch"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		if line.CWD != "" && info.WorkDir == "" {
			info.WorkDir = line.CWD
		}
		if line.GitBranch != "" && info.GitBranch == "" {
			info.GitBranch = line.GitBranch
		}
		if info.WorkDir != "" && info.GitBranch != "" {
			break
		}
	}
	return scanner.Err()
}

// decodeProjectDir is a best-effort decode of the encoded project directory
// name. The encoding is lossy (both / and . become -), so this is only
// used as a fallback when JSONL parsing fails.
func decodeProjectDir(encoded string) string {
	// Strip leading dash and replace remaining dashes with /.
	if strings.HasPrefix(encoded, "-") {
		encoded = encoded[1:]
	}
	return "/" + strings.ReplaceAll(encoded, "-", "/")
}

// IsActive checks if a session UUID is currently open by a claude process.
func (s *Scanner) IsActive(uuid string) bool {
	return s.activeUUIDs()[uuid]
}

// activeUUIDs returns the set of session UUIDs that are currently in use
// by a running claude process. Uses two strategies:
//  1. Match --resume <uuid> in process args (for resumed sessions)
//  2. Match process cwd to project directories (for fresh sessions)
//
// Results are cached for 5 seconds.
func (s *Scanner) activeUUIDs() map[string]bool {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()

	if s.activeCache != nil && time.Since(s.activeFetched) < 5*time.Second {
		return s.activeCache
	}

	result := make(map[string]bool)
	defer func() {
		s.activeCache = result
		s.activeFetched = time.Now()
	}()

	// Find all claude process PIDs by executable name.
	pidOut, err := exec.Command("pgrep", "-x", "claude").Output()
	if err != nil {
		return result // exit code 1 = no matches
	}

	pids := nonEmptyLines(string(pidOut))
	if len(pids) == 0 {
		return result
	}

	// Get full args for all claude processes in one call.
	psArgs := []string{"-ww", "-o", "pid=,args="}
	for _, pid := range pids {
		psArgs = append(psArgs, "-p", pid)
	}
	argsOut, err := exec.Command("ps", psArgs...).Output()
	if err != nil {
		return result
	}

	var unmatchedPIDs []string
	for _, line := range nonEmptyLines(string(argsOut)) {
		pid, args := splitPIDArgs(line)
		if pid == "" {
			continue
		}
		if uuid := extractResumeUUID(args); uuid != "" {
			result[uuid] = true
		} else {
			unmatchedPIDs = append(unmatchedPIDs, pid)
		}
	}

	// For processes without --resume, match by working directory.
	if len(unmatchedPIDs) > 0 {
		s.matchByCwd(unmatchedPIDs, result)
	}

	return result
}

// extractResumeUUID extracts a UUID from --resume <uuid> or --resume=<uuid>.
func extractResumeUUID(args string) string {
	for _, sep := range []string{"--resume ", "--resume="} {
		idx := strings.Index(args, sep)
		if idx < 0 {
			continue
		}
		rest := args[idx+len(sep):]
		token := rest
		if sp := strings.IndexByte(rest, ' '); sp >= 0 {
			token = rest[:sp]
		}
		if IsUUID(token) {
			return token
		}
	}
	return ""
}

// splitPIDArgs splits a "  PID ARGS..." line from ps -o pid=,args= output.
func splitPIDArgs(line string) (pid, args string) {
	line = strings.TrimSpace(line)
	sp := strings.IndexByte(line, ' ')
	if sp < 0 {
		return line, ""
	}
	return line[:sp], strings.TrimSpace(line[sp+1:])
}

// matchByCwd resolves active sessions for processes without --resume
// by matching their working directory to project JSONL files.
func (s *Scanner) matchByCwd(pids []string, result map[string]bool) {
	cwdByPID := getCwds(pids)

	// Group by cwd to handle multiple processes in the same directory.
	pidsByCwd := make(map[string]int)
	for _, cwd := range cwdByPID {
		pidsByCwd[cwd]++
	}

	for cwd, count := range pidsByCwd {
		encoded := encodeProjectDir(cwd)
		projPath := filepath.Join(s.baseDir, encoded)
		for _, uuid := range newestUnmatchedSessions(projPath, result, count) {
			result[uuid] = true
		}
	}
}

// getCwds returns a map of PID to cwd for the given process IDs,
// using lsof to resolve working directories.
func getCwds(pids []string) map[string]string {
	out, err := exec.Command("lsof", "-a", "-d", "cwd", "-Fn",
		"-p", strings.Join(pids, ",")).Output()
	if err != nil {
		return nil
	}
	return parseLsofCwds(out)
}

// parseLsofCwds parses lsof -Fn output into a PID to path map.
// Expected format: p<pid>\nfcwd\nn<path>\n repeated per process.
func parseLsofCwds(out []byte) map[string]string {
	result := make(map[string]string)
	var currentPID string
	expectPath := false

	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			currentPID = line[1:]
			expectPath = false
		case line == "fcwd":
			expectPath = true
		case expectPath && strings.HasPrefix(line, "n"):
			if currentPID != "" {
				result[currentPID] = line[1:]
			}
			expectPath = false
		}
	}
	return result
}

// encodeProjectDir encodes a filesystem path the same way Claude Code
// encodes project directory names under ~/.claude/projects/.
func encodeProjectDir(path string) string {
	var b strings.Builder
	for _, c := range path {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' {
			b.WriteRune(c)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// newestUnmatchedSessions returns up to n session UUIDs from projDir,
// choosing the most recently modified JSONL files not already in matched.
func newestUnmatchedSessions(projDir string, matched map[string]bool, n int) []string {
	entries, err := os.ReadDir(projDir)
	if err != nil {
		return nil
	}

	type candidate struct {
		uuid    string
		modTime time.Time
	}
	var candidates []candidate

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		uuid := strings.TrimSuffix(e.Name(), ".jsonl")
		if !IsUUID(uuid) || matched[uuid] {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{uuid, fi.ModTime()})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})

	var result []string
	for i := 0; i < n && i < len(candidates); i++ {
		result = append(result, candidates[i].uuid)
	}
	return result
}

// nonEmptyLines splits s by newlines and returns non-empty trimmed lines.
func nonEmptyLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// IsUUID checks if a string looks like a UUID (36 chars with hyphens at
// positions 8, 13, 18, 23).
func IsUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}
