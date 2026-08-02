package models

import "time"

// ProcedureFile is an attachment on a procedure record — a lab result,
// scan or PDF uploaded by a clinic or by the pet's owner.
//
// Only the storage object key is persisted, never a URL: the bucket is
// private and every download is served through a short-lived signed URL
// minted at request time. Storing a URL would either bake in an
// expiry that goes stale, or require the bucket to be public.
//
// PetID is denormalised from the owning procedure so that ownership can
// be checked without joining the (large, legacy) vaccination table.
type ProcedureFile struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	ProcedureID uint      `gorm:"column:procedure_id" json:"procedure_id"`
	PetID       string    `gorm:"column:pet_id" json:"pet_id"`
	ObjectKey   string    `gorm:"column:object_key" json:"-"` // never leaves the server
	FileName    string    `gorm:"column:file_name" json:"file_name"`
	ContentType string    `gorm:"column:content_type" json:"content_type"`
	SizeBytes   int64     `gorm:"column:size_bytes" json:"size_bytes"`
	UploadedBy  uint      `gorm:"column:uploaded_by" json:"uploaded_by"`
	CreatedAt   time.Time `gorm:"column:created_at" json:"created_at"`
}

func (ProcedureFile) TableName() string { return "procedure_files" }
