package pgpb

import (
	"testing"
)

func TestBackupLockKey(t *testing.T) {
	key := hashJobID("pgpb_backup")
	if key == 0 {
		t.Fatal("backup lock key should not be zero")
	}

	// Deterministic
	key2 := hashJobID("pgpb_backup")
	if key != key2 {
		t.Fatal("backup lock key should be deterministic")
	}

	// Different from restore
	restoreKey := hashJobID("pgpb_restore")
	if key == restoreKey {
		t.Fatal("backup and restore lock keys should differ")
	}
}
