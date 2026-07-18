package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestExtractClaudeCodeSessionIDFromPayloadJSON(t *testing.T) {
	payload := []byte(`{"metadata":{"user_id":"{\"device_id\":\"d\",\"session_id\":\"cache-session-1\"}"}}`)
	got := ExtractClaudeCodeSessionID(context.Background(), payload, nil)
	if got != "cache-session-1" {
		t.Fatalf("ExtractClaudeCodeSessionID() = %q, want cache-session-1", got)
	}
}

func TestExtractClaudeCodeSessionIDFromHeader(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	ginCtx.Request.Header.Set(ClaudeCodeSessionHeader, "header-session-1")
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	got := ExtractClaudeCodeSessionID(ctx, []byte(`{"model":"gpt-5.4"}`), nil)
	if got != "header-session-1" {
		t.Fatalf("ExtractClaudeCodeSessionID() = %q, want header-session-1", got)
	}
}

func TestExtractClaudeCodeAgentIDFromHeadersAndContext(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	ginCtx.Request.Header.Set(ClaudeCodeAgentHeader, "gin-agent")
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	if got := ExtractClaudeCodeAgentID(ctx, nil); got != "gin-agent" {
		t.Fatalf("ExtractClaudeCodeAgentID() from context = %q, want gin-agent", got)
	}
	headers := http.Header{}
	headers.Set(ClaudeCodeAgentHeader, " explicit-agent ")
	if got := ExtractClaudeCodeAgentID(ctx, headers); got != "explicit-agent" {
		t.Fatalf("ExtractClaudeCodeAgentID() explicit = %q, want explicit-agent", got)
	}
	if got := ExtractClaudeCodeAgentID(context.Background(), nil); got != "" {
		t.Fatalf("ExtractClaudeCodeAgentID() without header = %q, want empty", got)
	}
}

func TestClaudeCodePromptCacheStableAcrossRequests(t *testing.T) {
	ctx := context.Background()
	payload := []byte(`{"metadata":{"user_id":"{\"session_id\":\"cache-session-2\"}"}}`)
	first, ok, err := ClaudeCodePromptCache(ctx, "grok-composer-2.5-fast", payload, nil)
	if err != nil {
		t.Fatalf("ClaudeCodePromptCache first error: %v", err)
	}
	if !ok || first.ID == "" {
		t.Fatalf("ClaudeCodePromptCache first = %#v, ok=%v, want cached id", first, ok)
	}
	second, ok, err := ClaudeCodePromptCache(ctx, "grok-composer-2.5-fast", payload, nil)
	if err != nil {
		t.Fatalf("ClaudeCodePromptCache second error: %v", err)
	}
	if !ok || second.ID != first.ID {
		t.Fatalf("second cache id = %q, want %q", second.ID, first.ID)
	}
}

func TestClaudeCodePromptCacheStableWithoutProcessCacheState(t *testing.T) {
	ctx := context.Background()
	payload := []byte(`{"metadata":{"user_id":"{\"session_id\":\"durable-session\"}"}}`)

	first, ok, err := ClaudeCodePromptCache(ctx, "gpt-5.6-sol(xhigh)", payload, nil)
	if err != nil || !ok {
		t.Fatalf("ClaudeCodePromptCache first = %#v, ok=%v, err=%v", first, ok, err)
	}

	codexCacheMu.Lock()
	codexCacheMap = make(map[string]CodexCache)
	codexCacheMu.Unlock()

	second, ok, err := ClaudeCodePromptCache(ctx, "gpt-5.6-sol(xhigh)", payload, nil)
	if err != nil || !ok {
		t.Fatalf("ClaudeCodePromptCache second = %#v, ok=%v, err=%v", second, ok, err)
	}
	if second.ID != first.ID {
		t.Fatalf("cache ID after reset = %q, want %q", second.ID, first.ID)
	}
}

func TestClaudeCodePromptCacheSeparatesModels(t *testing.T) {
	payload := []byte(`{"metadata":{"user_id":"{\"session_id\":\"model-lane\"}"}}`)
	first, _, _ := ClaudeCodePromptCache(context.Background(), "gpt-5.6-sol(xhigh)", payload, nil)
	second, _, _ := ClaudeCodePromptCache(context.Background(), "gpt-5.6-terra(xhigh)", payload, nil)
	if first.ID == second.ID {
		t.Fatalf("different models share cache ID %q", first.ID)
	}
}

func TestClaudeCodePromptCacheSeparatesAgentsAndPreservesHeaderlessKey(t *testing.T) {
	payload := []byte(`{"metadata":{"user_id":"{\"session_id\":\"agent-lane\"}"}}`)
	model := "gpt-5.6-sol(xhigh)"
	agentAHeaders := http.Header{}
	agentAHeaders.Set(ClaudeCodeAgentHeader, "agent-a")
	agentBHeaders := http.Header{}
	agentBHeaders.Set(ClaudeCodeAgentHeader, "agent-b")

	agentA, ok, err := ClaudeCodePromptCache(context.Background(), model, payload, agentAHeaders)
	if err != nil || !ok {
		t.Fatalf("agent A prompt cache = %#v, ok=%v, err=%v", agentA, ok, err)
	}
	agentARepeat, _, _ := ClaudeCodePromptCache(context.Background(), model, payload, agentAHeaders)
	agentB, _, _ := ClaudeCodePromptCache(context.Background(), model, payload, agentBHeaders)
	if agentA.ID != agentARepeat.ID {
		t.Fatalf("same agent produced different prompt cache IDs: %q and %q", agentA.ID, agentARepeat.ID)
	}
	if agentA.ID == agentB.ID {
		t.Fatalf("different agents share prompt cache ID %q", agentA.ID)
	}

	headerless, ok, err := ClaudeCodePromptCache(context.Background(), model, payload, nil)
	if err != nil || !ok {
		t.Fatalf("headerless prompt cache = %#v, ok=%v, err=%v", headerless, ok, err)
	}
	legacyName := strings.Join([]string{"cli-proxy-api", "claude-code", "prompt-cache", model, "agent-lane"}, ":")
	wantHeaderless := uuid.NewSHA1(uuid.NameSpaceOID, []byte(legacyName)).String()
	if headerless.ID != wantHeaderless {
		t.Fatalf("headerless prompt cache ID = %q, want legacy ID %q", headerless.ID, wantHeaderless)
	}
}

func TestExtractClaudeCodeSessionIDPrefersHeaderOverPayload(t *testing.T) {
	payload := []byte(`{"metadata":{"user_id":"{"session_id":"payload-session"}"}}`)
	headers := http.Header{}
	headers.Set(ClaudeCodeSessionHeader, "header-session")

	got := ExtractClaudeCodeSessionID(context.Background(), payload, headers)
	if got != "header-session" {
		t.Fatalf("ExtractClaudeCodeSessionID() = %q, want header-session", got)
	}
}

func TestClaudeCodeExecutionScopeAcceptsLowercaseHeaderMapKeys(t *testing.T) {
	headers := http.Header{
		"x-claude-code-session-id": []string{"lower-session"},
		"x-claude-code-agent-id":   []string{"lower-agent"},
	}

	scope, ok := ClaudeCodeExecutionScope(context.Background(), nil, headers)
	if !ok || scope != "claude:lower-session:agent:lower-agent" {
		t.Fatalf("lowercase header scope = %q, %v", scope, ok)
	}
}

func TestClaudeCodeExecutionScopeIsolatesAgents(t *testing.T) {
	rootHeaders := http.Header{}
	rootHeaders.Set(ClaudeCodeSessionHeader, "session-agents")
	childAHeaders := rootHeaders.Clone()
	childAHeaders.Set(ClaudeCodeAgentHeader, "agent-a")
	childBHeaders := rootHeaders.Clone()
	childBHeaders.Set(ClaudeCodeAgentHeader, "agent-b")

	rootScope, ok := ClaudeCodeExecutionScope(context.Background(), nil, rootHeaders)
	if !ok || rootScope != "claude:session-agents:agent:main" {
		t.Fatalf("root scope = %q, %v", rootScope, ok)
	}
	childAScope, ok := ClaudeCodeExecutionScope(context.Background(), nil, childAHeaders)
	if !ok || childAScope != "claude:session-agents:agent:agent-a" {
		t.Fatalf("child A scope = %q, %v", childAScope, ok)
	}
	childBScope, ok := ClaudeCodeExecutionScope(context.Background(), nil, childBHeaders)
	if !ok || childBScope != "claude:session-agents:agent:agent-b" {
		t.Fatalf("child B scope = %q, %v", childBScope, ok)
	}
	if rootScope == childAScope || childAScope == childBScope || rootScope == childBScope {
		t.Fatalf("agent scopes are not isolated: root=%q a=%q b=%q", rootScope, childAScope, childBScope)
	}
}

func TestClaudeCodePromptCacheDeterministicAndAgentScoped(t *testing.T) {
	rootHeaders := http.Header{}
	rootHeaders.Set(ClaudeCodeSessionHeader, "session-cache-agents")
	childHeaders := rootHeaders.Clone()
	childHeaders.Set(ClaudeCodeAgentHeader, "agent-a")

	rootFirst, ok, errFirst := ClaudeCodePromptCache(context.Background(), "gpt-5.4", nil, rootHeaders)
	if errFirst != nil || !ok {
		t.Fatalf("root first cache = %#v, %v, %v", rootFirst, ok, errFirst)
	}
	rootSecond, ok, errSecond := ClaudeCodePromptCache(context.Background(), "gpt-5.4", nil, rootHeaders)
	if errSecond != nil || !ok || rootSecond.ID != rootFirst.ID {
		t.Fatalf("root second cache = %#v, %v, %v; want ID %q", rootSecond, ok, errSecond, rootFirst.ID)
	}
	child, ok, errChild := ClaudeCodePromptCache(context.Background(), "gpt-5.4", nil, childHeaders)
	if errChild != nil || !ok || child.ID == rootFirst.ID {
		t.Fatalf("child cache = %#v, %v, %v; root ID %q", child, ok, errChild, rootFirst.ID)
	}
	otherModel, ok, errModel := ClaudeCodePromptCache(context.Background(), "gpt-5.5", nil, rootHeaders)
	if errModel != nil || !ok || otherModel.ID == rootFirst.ID {
		t.Fatalf("other model cache = %#v, %v, %v; root ID %q", otherModel, ok, errModel, rootFirst.ID)
	}
}
