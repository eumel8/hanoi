package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	appconfig "hanoi/internal/config"
)

func memoryToolResult(t *testing.T, name, args string) memoryResult {
	t.Helper()
	text, err := callMemoryTool(context.Background(), name, args)
	if err != nil {
		t.Fatal(err)
	}
	var result memoryResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMemoryArchiveMigrationAndTimestamps(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir(".hanoi", 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `[{"role":"user","content":"Legacy question"}]`
	if err := os.WriteFile(memoryPath("legacy"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	messages, err := loadMemory("legacy")
	if err != nil || len(messages) != 1 || messages[0].Timestamp != nil {
		t.Fatalf("legacy load: %+v %v", messages, err)
	}
	messages = appendMessage(messages, "", message{Role: "user", Content: "New question"})
	if messages[0].Timestamp != nil || messages[1].Timestamp == nil {
		t.Fatal("new timestamps missing or legacy event time fabricated")
	}
	if err := saveMemory("legacy", messages); err != nil {
		t.Fatal(err)
	}
	archive, err := readMemoryArchive("legacy")
	if err != nil || archive.Version != 1 || archive.CreatedAt.IsZero() || archive.UpdatedAt.IsZero() || !archive.Messages[1].Timestamp.Equal(*messages[1].Timestamp) {
		t.Fatalf("archive: %+v %v", archive, err)
	}
	created := archive.CreatedAt
	if err := saveMemory("legacy", archive.Messages); err != nil {
		t.Fatal(err)
	}
	again, err := readMemoryArchive("legacy")
	if err != nil || !again.CreatedAt.Equal(created) || !again.Messages[1].Timestamp.Equal(*messages[1].Timestamp) {
		t.Fatal("save changed creation or message time")
	}
	for _, data := range []string{`{"version":99,"messages":[]}`, `broken`} {
		if err := os.WriteFile(memoryPath("future"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := saveMemory("future", nil); err == nil {
			t.Fatal("overwrote unsupported or corrupt archive")
		}
	}
}

func TestMemorySearchDatesPaginationAndClear(t *testing.T) {
	t.Chdir(t.TempDir())
	now := time.Now()
	yesterday := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location()).AddDate(0, 0, -1)
	older := yesterday.AddDate(0, 0, -1)
	for name, messages := range map[string][]message{
		"one": {{Role: "system", Content: "private rules"}, {Role: "user", Content: "Fix CRA issue", Timestamp: &yesterday}, {Role: "assistant", Content: "CRA fixed", Timestamp: &now}},
		"two": {{Role: "assistant", Content: "CRA issue investigated", Timestamp: &older}, {Role: "assistant", Content: "CRA issue resolved", Timestamp: &yesterday}, {Role: "user", Content: "CRA issue legacy"}},
	} {
		if err := saveMemory(name, messages); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(memoryPath("broken"), []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := memoryToolResult(t, "memory_search", `{"day":"yesterday","query":"cra ISSUE","limit":1}`)
	if result.Total != 2 || len(result.Results) != 1 || result.NextOffset == nil || *result.NextOffset != 1 || result.UndatedMessages != 1 || len(result.SkippedSessions) != 1 || result.Results[0].Session != "one" {
		t.Fatalf("search: %+v", result)
	}
	result = memoryToolResult(t, "memory_search", `{"day":"yesterday","query":"cra issue","limit":1,"offset":1}`)
	if len(result.Results) != 1 || result.Results[0].Session != "two" || result.NextOffset != nil {
		t.Fatalf("pagination: %+v", result)
	}
	result = memoryToolResult(t, "memory_read", `{"session":"one","offset":0,"limit":1}`)
	if len(result.Results) != 1 || result.Results[0].MessageIndex != 1 || result.NextOffset == nil || *result.NextOffset != 2 {
		t.Fatalf("read context: %+v", result)
	}
	result = memoryToolResult(t, "memory_search", `{"query":"legacy","session":"two"}`)
	if result.Total != 1 || !result.Results[0].TimeUnknown || result.Results[0].Timestamp != nil {
		t.Fatalf("undated recall: %+v", result)
	}
	if err := saveMemory("one", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(memoryPath("two")); err != nil {
		t.Fatal(err)
	}
	result = memoryToolResult(t, "memory_search", `{}`)
	if result.Total != 0 {
		t.Fatal("cleared or deleted memory survived")
	}
	// A sibling workspace has its own memory scope.
	t.Chdir(t.TempDir())
	if result := memoryToolResult(t, "memory_search", `{}`); result.Total != 0 {
		t.Fatal("cross-workspace memory leak")
	}
}

func TestMemoryBoundsDSTAndValidation(t *testing.T) {
	location, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 30, 12, 0, 0, 0, location)
	from, until, err := memoryBounds(memoryArgs{Day: "yesterday"}, now)
	if err != nil || until.Sub(*from) != 23*time.Hour || from.Day() != 29 {
		t.Fatalf("calendar day across DST: %v %v %v", from, until, err)
	}
	for _, args := range []string{`null`, `{"day":"bad"}`, `{"day":"today","from":"2026-01-01"}`, `{"from":"2026-02-01","until":"2026-01-01"}`, `{"limit":51}`, `{"offset":-1}`, `{"session":"../outside"}`, `{"unknown":true}`, `{} {}`} {
		if _, err := callMemoryTool(context.Background(), "memory_search", args); err == nil {
			t.Errorf("accepted %s", args)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := callMemoryTool(ctx, "memory_read", `{"session":"missing"}`); err == nil {
		t.Fatal("expected cancellation or missing archive error")
	}
}

func TestMemorySearchRangeAndBoundedPages(t *testing.T) {
	t.Chdir(t.TempDir())
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	messages := []message{{Role: "user", Content: "at end", Timestamp: &end}}
	for i := 0; i < 50; i++ {
		messages = append(messages, message{Role: "assistant", Content: strings.Repeat("界", 400), Timestamp: &start})
	}
	if err := saveMemory("many", messages); err != nil {
		t.Fatal(err)
	}
	count, offset := 0, 0
	for {
		args := fmt.Sprintf(`{"from":"2026-11-10T00:00:00Z","until":"2026-11-11T00:00:00Z","limit":50,"offset":%d}`, offset)
		text, err := callMemoryTool(context.Background(), "memory_search", args)
		if err != nil || len(text) > 24*1024 {
			t.Fatalf("unbounded result: %d bytes, %v", len(text), err)
		}
		var result memoryResult
		if err := json.Unmarshal([]byte(text), &result); err != nil {
			t.Fatal(err)
		}
		if result.Total != 50 || len(result.Results) == 0 {
			t.Fatalf("incorrect interval or page: %+v", result)
		}
		count += len(result.Results)
		if result.NextOffset == nil {
			break
		}
		if *result.NextOffset <= offset {
			t.Fatal("pagination did not advance")
		}
		offset = *result.NextOffset
	}
	if count != 50 {
		t.Fatalf("pagination lost results: %d", count)
	}
}

func TestMemoryLongUnicodeAndToolRoundTrip(t *testing.T) {
	t.Chdir(t.TempDir())
	text := strings.Repeat("界", 10000)
	messages := appendMessage(nil, "", message{Role: "user", Content: text})
	if err := saveMemory("long", messages); err != nil {
		t.Fatal(err)
	}
	var reconstructed strings.Builder
	offset := 0
	for {
		result := memoryToolResult(t, "memory_read", fmt.Sprintf(`{"session":"long","content_offset":%d}`, offset))
		hit := result.Results[0]
		reconstructed.WriteString(hit.Text)
		if hit.NextContentOffset == nil {
			break
		}
		offset = *hit.NextContentOffset
	}
	if reconstructed.String() != text {
		t.Fatal("long Unicode message lost content")
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		for _, msg := range request.Messages {
			if msg.Timestamp != nil {
				t.Error("archive timestamp sent to provider")
			}
		}
		if requests == 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"recall","type":"function","function":{"name":"memory_search","arguments":"{}"}}]}}]}`)
			return
		}
		if last := request.Messages[len(request.Messages)-1]; last.Role != "tool" || !strings.Contains(last.Content, `"session":"long"`) {
			t.Errorf("memory tool result missing: %+v", last)
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"recalled"}}]}`)
	}))
	defer server.Close()
	answer, err := sendChat(config{Config: appconfig.Config{Model: "test", BaseURL: server.URL}}, messages, false)
	if err != nil || answer != "recalled" || requests != 2 || messages[0].Timestamp == nil {
		t.Fatalf("round trip: %q %v", answer, err)
	}
}
