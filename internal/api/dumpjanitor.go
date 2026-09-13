package api

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/erdemkayatr/nabiz/internal/identity"
)

// DumpRetention, dump dosyalarının saklama kuralları.
type DumpRetention struct {
	// MaxAge, bu yaştan eski dosyalar silinir.
	MaxAge time.Duration
	// MaxBytes, toplam disk kotası. Aşılırsa en eskiden başlayarak silinir.
	MaxBytes int64
	// Interval, temizlik turları arasındaki süre.
	Interval time.Duration
}

// DefaultDumpRetention, makul varsayılanlar.
//
// Bellek dump'ları yüzlerce MB; bir kota olmadan nabiz-api'nin diski, izlediği
// sistemden önce dolar.
func DefaultDumpRetention() DumpRetention {
	return DumpRetention{
		MaxAge:   7 * 24 * time.Hour,
		MaxBytes: 10 << 30, // 10 GiB
		Interval: time.Hour,
	}
}

// StartDumpJanitor, saklama kurallarını uygulayan döngüyü başlatır.
func (s *Server) StartDumpJanitor(ctx context.Context) {
	go func() {
		// Açılışta bir kez koş: süreç kapalıyken biriken dosyalar beklemesin.
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

// cleanDumps, yaş ve kota kurallarını uygular.
func (s *Server) cleanDumps(ctx context.Context) {
	artifacts, err := s.identity.ListArtifacts(ctx, nil, 10000)
	if err != nil {
		s.log.Error("dump temizliği için kayıtlar okunamadı", "err", err)
		return
	}

	cutoff := time.Now().Add(-s.retention.MaxAge)
	var removedAge, removedQuota int
	var total int64

	// Liste yeniden eskiye sıralı geliyor; yaşı geçenleri at, kalanların
	// toplamını biriktir.
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

	// Kota aşıldıysa en eskiden başlayarak sil.
	for i := len(kept) - 1; i >= 0 && total > s.retention.MaxBytes; i-- {
		a := &artifacts[kept[i]]
		s.removeDump(ctx, a.ID, a.Filename)
		total -= a.Bytes
		removedQuota++
	}

	// Kaydı olmayan yetim dosyalar: bir çekme yarıda kaldıysa disk dolu
	// kalmasın.
	s.removeOrphans(artifacts)

	if removedAge > 0 || removedQuota > 0 {
		s.log.Info("dump temizliği",
			"yas_asimi", removedAge, "kota_asimi", removedQuota,
			"kalan_bayt", total, "kota", s.retention.MaxBytes)
	}
}

func (s *Server) removeDump(ctx context.Context, id, filename string) {
	if filename != "" {
		if err := os.Remove(filepath.Join(s.dumpDir, filepath.Base(filename))); err != nil && !os.IsNotExist(err) {
			s.log.Warn("dump dosyası silinemedi", "file", filename, "err", err)
		}
	}
	if err := s.identity.DeleteArtifact(ctx, id); err != nil {
		s.log.Warn("dump kaydı silinemedi", "artifact", id, "err", err)
	}
}

// removeOrphans, veritabanında karşılığı olmayan dosyaları siler.
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
		// Yeni yazılmakta olan bir dosyayı silmeyelim.
		if err != nil || time.Since(info.ModTime()) < 30*time.Minute {
			continue
		}
		if err := os.Remove(filepath.Join(s.dumpDir, entry.Name())); err == nil {
			s.log.Info("kaydı olmayan dump dosyası silindi", "file", entry.Name())
		}
	}
}

// dumpUsage, diskteki toplam kullanımı hesaplar.
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
