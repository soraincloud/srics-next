package backup

import (
	"fmt"
	"testing"
	"time"
)

func TestRetentionKeepsNewestDailyMonthlyUnion(t *testing.T) {
	s := []Snapshot{}
	for i, date := range []string{"2026-09-20T18:00:00Z", "2026-09-20T10:00:00Z", "2026-09-19T10:00:00Z", "2026-09-18T10:00:00Z", "2026-08-01T10:00:00Z", "2026-07-01T10:00:00Z"} {
		d, _ := time.Parse(time.RFC3339, date)
		s = append(s, Snapshot{ID: fmt.Sprintf("%064d", i), Time: d})
	}
	p, e := retentionPlan(s, Retention{true, 2, 2})
	if e != nil || len(p.Keep) != 3 || len(p.Remove) != 3 {
		t.Fatal(p, e)
	}
	if p.Keep[0].ID != s[0].ID || p.Keep[1].ID != s[2].ID || p.Keep[2].ID != s[4].ID {
		t.Fatal("wrong union", p)
	}
	q, _ := retentionPlan(s, Retention{true, 1, 0})
	if p.Token == q.Token {
		t.Fatal("policy change not bound")
	}
	if _, e = retentionPlan(s, Retention{true, 0, 0}); e == nil {
		t.Fatal("empty retention allowed")
	}
}
