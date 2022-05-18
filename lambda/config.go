package main

import (
	"os"
	"time"
)

const (
	LIFESPAN = 5 * time.Minute
)

var (
	S3_COLLECTOR_BUCKET string = "sionreview.datapool"
	S3_BACKUP_BUCKET    string = "sion.backup%s" // Leave "%s" alone at the end of the bucket.

	DRY_RUN = false
)

func init() {
	// Set required
	S3_COLLECTOR_BUCKET = GetenvIf(os.Getenv("S3_COLLECTOR_BUCKET"), S3_COLLECTOR_BUCKET)

	// Set required
	S3_BACKUP_BUCKET = GetenvIf(os.Getenv("S3_BACKUP_BUCKET"), S3_BACKUP_BUCKET)
}

func GetenvIf(env string, def string) string {
	if len(env) > 0 {
		return env
	} else {
		return def
	}
}
