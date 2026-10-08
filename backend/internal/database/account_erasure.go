package database

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AccountErasure intentionally stores neither the identity nor its email. The
// digest remains pseudonymous (not anonymous) and has no automatic expiry.
// EffectiveUserID already unifies migrated Clerk and Keycloak identities.
type AccountErasure struct {
	Digest   string    `gorm:"primaryKey;size:64"`
	ErasedAt time.Time `gorm:"not null"`
}

var ErrAccountErased = errors.New("monitoring account erased")

func accountErasureDigest(userID string) [32]byte {
	return sha256.Sum256([]byte("gnomonitoring:account-erasure:v1\x00" + userID))
}

// withAccountLock serializes one identity across PostgreSQL connections and
// processes, including an identity that has no user row yet. Lock collisions
// only serialize unrelated identities: marker lookup uses the FULL digest.
// Read committed ensures the marker read after waiting sees the previous
// owner's commit. Callers must never acquire locks for another identity inside
// this callback, or wrap it in a transaction with an older snapshot.
func withAccountLock(db *gorm.DB, userID string, run func(*gorm.DB, string) error) error {
	if userID == "" {
		return errors.New("empty account identity")
	}
	if db.Dialector.Name() != "postgres" {
		return errors.New("account erasure requires PostgreSQL")
	}
	digest := accountErasureDigest(userID)
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(binary.BigEndian.Uint64(digest[:8]))).Error; err != nil {
			return err
		}
		// Nested transactions do not honor TxOptions; refuse an inherited snapshot
		// which could miss a marker committed while this transaction waited.
		var isolation string
		if err := tx.Raw("SHOW transaction_isolation").Scan(&isolation).Error; err != nil {
			return err
		}
		if isolation != "read committed" {
			return fmt.Errorf("account writes require read committed isolation, got %s", isolation)
		}
		return run(tx, hex.EncodeToString(digest[:]))
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

func withActiveAccountWrite(db *gorm.DB, userID string, run func(*gorm.DB) error) error {
	return withAccountLock(db, userID, func(tx *gorm.DB, digest string) error {
		var count int64
		if err := tx.Model(&AccountErasure{}).Where("digest = ?", digest).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrAccountErased
		}
		return run(tx)
	})
}

// EraseAccount is distinct from DeleteUser (the historical reset). Marker and
// cascading data removal commit together; a failure rolls BOTH back. Repeated
// erasure is idempotent and also handles identities never provisioned here.
func EraseAccount(db *gorm.DB, userID string) error {
	return withAccountLock(db, userID, func(tx *gorm.DB, digest string) error {
		marker := AccountErasure{Digest: digest, ErasedAt: time.Now().UTC()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&marker).Error; err != nil {
			return err
		}
		return deleteUserRows(tx, userID)
	})
}

// IsAccountErased is an early UI/API rejection only, never the write fence.
func IsAccountErased(db *gorm.DB, userID string) (bool, error) {
	digest := accountErasureDigest(userID)
	var count int64
	err := db.Model(&AccountErasure{}).Where("digest = ?", hex.EncodeToString(digest[:])).Count(&count).Error
	return count != 0, err
}
