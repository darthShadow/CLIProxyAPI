package helps

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseCodexResetCreditsKeepsAvailableExpiries(t *testing.T) {
	body := `{"available_count":3,"credits":[{"id":"fake-id","status":"available","expires_at":"2026-11-24T10:00:00Z","title":"fake-title","description":"fake-description","profile_user_id":"fake-user","profile_image_url":"fake-image"},{"status":"available","expires_at":null},{"status":"available","expires_at":"2026-10-30T10:00:00Z"},{"status":"redeemed","expires_at":"2026-10-26T10:00:00Z"}]}`
	n, exp, e := ParseCodexResetCredits([]byte(body))
	if e != nil {
		t.Fatalf("stub error=%v", e)
	}
	if n != 3 || len(exp) != 3 || exp[0] == nil || exp[1] == nil || exp[2] != nil {
		t.Fatalf("count=%d expiries=%v", n, exp)
	}
	want5 := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	want30 := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	if !exp[0].Equal(want5) || !exp[1].Equal(want30) {
		t.Errorf("wrong expiry ordering %v", exp)
	}
	for _, fake := range []string{"fake-id", "fake-title", "fake-description", "fake-user", "fake-image"} {
		if strings.Contains(fmt.Sprintf("%#v", exp), fake) {
			t.Errorf("retained %q", fake)
		}
	}
	n, exp, e = ParseCodexResetCredits([]byte(strings.Replace(body, `"available_count":3`, `"available_count":1`, 1)))
	if e != nil || n != 1 || len(exp) != 1 || !exp[0].Equal(want5) {
		t.Errorf("count trim failed: %d %v %v", n, exp, e)
	}
	if _, _, e := ParseCodexResetCredits([]byte(strings.Replace(body, "2026-10-30T10:00:00Z", "invalid", 1))); e == nil {
		t.Error("invalid expiry accepted")
	}
}
