package api

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/erdemkayatr/nabiz/internal/identity"
)

// DumpRetention holds the retention rules for dump files.
type DumpRetention struct {
	// MaxAge: files older than this are deleted.
	MaxAge time.Duration
	// MaxBytes is the total disk quota. When exceeded, the oldest go first.
	MaxBytes int64
	// Interval is the time between cleanup rounds.
	Interval time.Duration
}

// DefaultDumpRetention returns sensible defaults.
//
// Memory dumps run to hundreds of megabytes; without a quota, nabiz-api's disk
// fills up before the system it monitors.
func DefaultDumpRetention() DumpRetention {
	return DumpRetention{
		MaxAge:   7 * 24 * time.Hour,
		MaxBytes: 10 << 30, // 10 GiB
		Interval: time.Hour,
	}
}

// StartDumpJanitor launches the loop that enforces the retention rules.
func (s *Server) StartDumpJanitor(ctx context.Context) {
	go func() {
		// Run once at startup, so files that piled up while the process was down do not wait.
		s.cleanDumps(ctx)

		ticker := time.NewTicker(s.retention.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.cleanDumps(ctx)
			}
		}
	}()
}

// cleanDumps applies the age and quota rules.
func (s *Server) cleanDumps(ctx context.Context) {
	artifacts, err := s.identity.ListArtifacts(ctx, nil, 10000)
	if err != nil {
		s.log.Error("could not read the records for dump cleanup", "err", err)
		return
	}

	cutoff := time.Now().Add(-s.retention.MaxAge)
	var removedAge, removedQuota int
	var total int64

	// The list arrives newest-first; drop what is past its age and accumulate
	// the total of what remains.
	kept := make([]int, 0, len(artifacts))
	for i := range artifacts {
		a := &artifacts[i]
		if a.CreatedAt.Before(cutoff) {
			s.removeDump(ctx, a.ID, a.Filename)
			removedAge++
			continue
		}
		total += a.Bytes
		kept = append(kept, i)
	}

	// If the quota is exceeded, delete starting from the oldest.
	for i := len(kept) - 1; i >= 0 && total > s.retention.MaxBytes; i-- {
		a := &artifacts[kept[i]]
		s.removeDump(ctx, a.ID, a.Filename)
		total -= a.Bytes
		removedQuota++
	}

	// Orphaned files with no record: a fetch that was cut short must not leave
	// the disk full.
	s.removeOrphans(artifacts)

	if removedAge > 0 || removedQuota > 0 {
		s.log.Info("dump cleanup",
			"expired", removedAge, "over_quota", removedQuota,
			"remaining_bytes", total, "quota", s.retention.MaxBytes)
	}
}

func (s *Server) removeDump(ctx context.Context, id, filename string) {
	if filename != "" {
		if err := os.Remove(filepath.Join(s.dumpDir, filepath.Base(filename))); err != nil && !os.IsNotExist(err) {
			s.log.Warn("could not delete the dump file", "file", filename, "err", err)
		}
	}
	if err := s.identity.DeleteArtifact(ctx, id); err != nil {
		s.log.Warn("could not delete the dump record", "artifact", id, "err", err)
	}
}

// removeOrphans deletes files that have no counterpart in the database.
func (s *Server) removeOrphans(artifacts []identity.DumpArtifact) {
	known := make(map[string]struct{}, len(artifacts))
	for i := range artifacts {
		if artifacts[i].Filename != "" {
			known[artifacts[i].Filename] = struct{}{}
		}
	}
	entries, err := os.ReadDir(s.dumpDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, ok := known[entry.Name()]; ok {
			continue
		}
		info, err := entry.Info()
		// Never delete a file that is still being written.
		if err != nil || time.Since(info.ModTime()) < 30*time.Minute {
			continue
		}
		if err := os.Remove(filepath.Join(s.dumpDir, entry.Name())); err == nil {
			s.log.Info("deleted a dump file with no record", "file", entry.Name())
		}
	}
}

// dumpUsage computes the total usage on disk.
func (s *Server) dumpUsage() (bytes int64, files int) {
	entries, err := os.ReadDir(s.dumpDir)
	if err != nil {
		return 0, 0
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if info, err := entry.Info(); err == nil {
			bytes += info.Size()
			files++
		}
	}
	return bytes, files
}
