package bidgen

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

type Store struct{ db *gorm.DB }

func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) Create(ctx context.Context, task *Task) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&Task{}).Where("tenant_id = ? AND session_id = ? AND status NOT IN ?", task.TenantID, task.SessionID,
			[]Status{StatusCompleted, StatusCancelled}).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrActiveTask
		}
		return tx.Create(task).Error
	})
	if err != nil && (errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(strings.ToLower(err.Error()), "unique constraint") ||
		strings.Contains(strings.ToLower(err.Error()), "duplicate key")) {
		return ErrActiveTask
	}
	return err
}

// Get is for trusted workers/adapters. HTTP handlers must first establish the
// session owner or use the service's tenant/session/user-scoped methods.
func (s *Store) Get(ctx context.Context, taskID string) (*Task, error) {
	var task Task
	if err := s.db.WithContext(ctx).First(&task, "id = ?", taskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &task, nil
}

func (s *Store) current(ctx context.Context, tenantID uint64, sessionID, userID string) (*Task, error) {
	var task Task
	err := s.db.WithContext(ctx).Where("tenant_id = ? AND session_id = ? AND user_id = ?", tenantID, sessionID, userID).
		Order("CASE WHEN status IN ('completed','cancelled') THEN 1 ELSE 0 END").Order("created_at DESC").Order("id DESC").First(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func (s *Store) save(ctx context.Context, task *Task, revision int64, leaseOwner *string) error {
	query := s.db.WithContext(ctx).Model(&Task{}).Where("id = ? AND revision = ?", task.ID, revision)
	if leaseOwner != nil {
		query = query.Where("lease_owner = ?", *leaseOwner)
	}
	now := time.Now().UTC()
	result := query.Updates(map[string]interface{}{
		"state": task.State, "status": task.Status, "last_error": task.LastError,
		"revision": revision + 1, "updated_at": now,
		"lease_owner": task.LeaseOwner, "lease_expires_at": task.LeaseExpiresAt,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrStaleRevision
	}
	task.Revision, task.UpdatedAt = revision+1, now
	return nil
}

func (s *Store) claim(ctx context.Context, taskID, owner string, ttl time.Duration) (*Task, error) {
	now, expires := time.Now().UTC(), time.Now().UTC().Add(ttl)
	var claimed Task
	var rows int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&Task{}).
			Where("id = ? AND status IN ? AND (lease_expires_at IS NULL OR lease_expires_at < ?)", taskID,
				[]Status{StatusPlanning, StatusRunning}, now).
			Updates(map[string]interface{}{"lease_owner": owner, "lease_expires_at": expires, "revision": gorm.Expr("revision + 1"), "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		rows = result.RowsAffected
		if rows == 1 {
			return tx.First(&claimed, "id = ?", taskID).Error
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		task, err := s.Get(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if !runnable(task.Status) {
			return nil, nil
		}
		return nil, ErrLeaseHeld
	}
	return &claimed, nil
}

func (s *Store) renew(ctx context.Context, taskID, owner string, ttl time.Duration) error {
	expires := time.Now().UTC().Add(ttl)
	result := s.db.WithContext(ctx).Model(&Task{}).Where("id = ? AND lease_owner = ? AND status NOT IN ?", taskID, owner,
		[]Status{StatusCompleted, StatusCancelled}).Update("lease_expires_at", expires)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrLeaseHeld
	}
	return nil
}

func (s *Store) recoverable(ctx context.Context) ([]Task, error) {
	var tasks []Task
	err := s.db.WithContext(ctx).Where("status IN ? AND (lease_expires_at IS NULL OR lease_expires_at < ?)",
		[]Status{StatusPlanning, StatusRunning}, time.Now().UTC()).Order("updated_at ASC").Limit(1000).Find(&tasks).Error
	return tasks, err
}

func runnable(status Status) bool { return status == StatusPlanning || status == StatusRunning }
