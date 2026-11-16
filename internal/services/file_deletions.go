package services

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"
)

// DrainFileDeletions removes queued private attachments, retaining failed
// items for the next attempt. No credentials or object keys enter logs.
func DrainFileDeletions(db *gorm.DB, storage *StorageService) error {
	if storage == nil || !storage.Enabled() {
		return nil
	}
	var keys []string
	if err := db.Raw(`SELECT object_key FROM vetapp_private.file_deletions ORDER BY created_at LIMIT 50`).Scan(&keys).Error; err != nil {
		return err
	}
	failed := 0
	for _, key := range keys {
		if err := storage.Delete(key); err != nil {
			failed++
			continue
		}
		if err := db.Exec(`DELETE FROM vetapp_private.file_deletions WHERE object_key = ?`, key).Error; err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d file deletions awaiting storage retry", failed)
	}
	return nil
}

func StartFileDeletionWorker(ctx context.Context, db *gorm.DB, storage *StorageService) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			if err := DrainFileDeletions(db, storage); err != nil {
				slog.Error("file deletion queue unavailable")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
