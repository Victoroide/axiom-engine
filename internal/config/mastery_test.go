package config

import "testing"

func TestMasteryLimitRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"0", "-1", "1.5", "many"} {
		_, err := loadWithoutAuth(func(k string) string {
			if k == "MAX_MASTERED_TOPICS" {
				return value
			}
			return ""
		})
		if err == nil {
			t.Errorf("accepted MAX_MASTERED_TOPICS=%q", value)
		}
	}
}

func TestMasteryLimitDefaultsAndOverride(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
	}{{"", 100}, {"2", 2}} {
		got, err := loadWithoutAuth(func(k string) string {
			if k == "MAX_MASTERED_TOPICS" {
				return tc.value
			}
			return ""
		})
		if err != nil || got.Mastery.MaxTopics != tc.want {
			t.Fatalf("config=%+v err=%v", got.Mastery, err)
		}
	}
}
