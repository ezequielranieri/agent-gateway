package crypto

import (
	"testing"
)

func TestVerifyPassword_Manual(t *testing.T) {
	hash := "$argon2id$v=19$m=65536,t=1,p=4$cXiBQoRMDJgwvhWg2UB/FQ$/kfZnv4ydcdWPtay735HYqRIAongOV2r7JHcI1ZZpQg"
	// Correct order: VerifyPassword(password, phcHash)
	err := VerifyPassword("test12345", hash)
	if err != nil {
		t.Fatalf("VERIFY FAILED: %v", err)
	}
	t.Log("VERIFY OK")
}
