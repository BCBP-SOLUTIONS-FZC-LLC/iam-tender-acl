package http

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── parseAsyncSpec ────────────────────────────────────────────────────────────

func TestParseAsyncSpec_HappyPath(t *testing.T) {
	s, err := parseAsyncSpec([]byte("asyncapi: '3.0.0'\ninfo:\n  title: t\n  version: v\n"))
	require.NoError(t, err)
	require.NotNil(t, s)
	assert.Equal(t, "3.0.0", s.AsyncAPI)
	assert.Equal(t, "t", s.Info.Title)
}

func TestParseAsyncSpec_InvalidYAML_ReturnsError(t *testing.T) {
	_, err := parseAsyncSpec([]byte("\t\tnot: valid\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse AsyncAPI spec")
}

// TestParseAsyncSpec_RealEmbeddedSpec exercises the actual embedded
// api/asyncapi.yaml (via loadAsyncSpec, which sync.Once-wraps this same
// parse) end to end — this service is receive-only (TAC-EVT-1), so both
// TenantOffboarded and TenantMembershipRemoved must come back tagged
// consumed, and there must be zero published messages.
func TestLoadAsyncSpec_RealEmbeddedSpec_BothMessagesConsumed(t *testing.T) {
	spec, err := loadAsyncSpec()
	require.NoError(t, err)
	require.NotNil(t, spec)

	pub, con := splitMessagesByDirection(spec.Comps.Messages)
	assert.Empty(t, pub, "this service publishes zero events (TAC-EVT-1)")
	assert.ElementsMatch(t, []string{"TenantOffboarded", "TenantMembershipRemoved"}, con)
}

// ── asyncMessage.isConsumed / splitMessagesByDirection ────────────────────────

func TestIsConsumed_NoTags_ReturnsFalse(t *testing.T) {
	msg := &asyncMessage{}
	assert.False(t, msg.isConsumed())
}

func TestIsConsumed_PublishedTag_ReturnsFalse(t *testing.T) {
	msg := &asyncMessage{Tags: []asyncRef{{Ref: "#/components/tags/published"}}}
	assert.False(t, msg.isConsumed())
}

func TestIsConsumed_ConsumedTag_ReturnsTrue(t *testing.T) {
	msg := &asyncMessage{Tags: []asyncRef{{Ref: "#/components/tags/consumed"}}}
	assert.True(t, msg.isConsumed())
}

func TestSplitMessagesByDirection_AllConsumed_PublishedEmpty(t *testing.T) {
	pub, con := splitMessagesByDirection(map[string]asyncMessage{
		"TenantMembershipRemoved": {Tags: []asyncRef{{Ref: "#/components/tags/consumed"}}},
		"TenantOffboarded":        {Tags: []asyncRef{{Ref: "#/components/tags/consumed"}}},
	})
	assert.Empty(t, pub)
	assert.Equal(t, []string{"TenantMembershipRemoved", "TenantOffboarded"}, con)
}

// ── renderMessage ─────────────────────────────────────────────────────────────

func TestRenderMessage_ConsumedTag_RendersReceiveBadge(t *testing.T) {
	var buf bytes.Buffer
	msg := &asyncMessage{
		Title: "Tenant Offboarded",
		Tags:  []asyncRef{{Ref: "#/components/tags/consumed"}},
	}
	renderMessage(&buf, "TenantOffboarded", msg, &asyncComponents{})
	out := buf.String()
	assert.Contains(t, out, `class="badge m-recv">RECEIVE`)
	assert.NotContains(t, out, `class="badge m-send">SEND`)
}

// ── renderPage: direction-section omission (this service's own shape) ────────

// TestRenderPage_NoPublishedMessages_OmitsPublishedSection is the regression
// test specific to this port: this service has zero published messages, so
// the "Published Messages" heading — which iam-user-profile's identical
// viewer always renders, even empty — must not appear at all here rather
// than showing an empty section.
func TestRenderPage_NoPublishedMessages_OmitsPublishedSection(t *testing.T) {
	spec := &asyncSpec{
		Info: asyncInfo{Title: "Test", Version: "1.0"},
		Comps: asyncComponents{
			Messages: map[string]asyncMessage{
				"TenantOffboarded": {Title: "Tenant Offboarded", Tags: []asyncRef{{Ref: "#/components/tags/consumed"}}},
			},
			Schemas: map[string]asyncSchema{},
		},
	}
	var buf bytes.Buffer
	renderPage(&buf, spec, "dev")
	out := buf.String()
	assert.NotContains(t, out, "Published Messages")
	assert.Contains(t, out, "Consumed Messages")
	assert.Contains(t, out, `id="msg-TenantOffboarded"`)
}

func TestRenderPage_NoMessagesAtAll_OmitsBothSections(t *testing.T) {
	spec := &asyncSpec{
		Info:  asyncInfo{Title: "Test", Version: "1.0"},
		Comps: asyncComponents{Messages: map[string]asyncMessage{}, Schemas: map[string]asyncSchema{}},
	}
	var buf bytes.Buffer
	renderPage(&buf, spec, "dev")
	out := buf.String()
	assert.NotContains(t, out, "Published Messages")
	assert.NotContains(t, out, "Consumed Messages")
}

// Same regression-class guard the underlying renderer already carries from
// iam-user-profile: every message/schema in the map must render a card,
// not just ones a hardcoded allowlist happens to name.
func TestRenderPage_AllMessagesAndSchemasRender_RegardlessOfName(t *testing.T) {
	spec := &asyncSpec{
		Info: asyncInfo{Title: "Test", Version: "1.0"},
		Comps: asyncComponents{
			Messages: map[string]asyncMessage{
				"SomeBrandNewConsumedEvent": {Title: "Some Brand New Consumed Event", Tags: []asyncRef{{Ref: "#/components/tags/consumed"}}},
			},
			Schemas: map[string]asyncSchema{
				"SomeBrandNewSchema": {Properties: map[string]asyncProp{"id": {Type: "string"}}},
			},
		},
	}
	var buf bytes.Buffer
	renderPage(&buf, spec, "dev")
	out := buf.String()
	assert.Contains(t, out, `id="msg-SomeBrandNewConsumedEvent"`)
	assert.Contains(t, out, `id="schema-SomeBrandNewSchema"`)
}

func TestRenderPage_LongDescription_Truncated(t *testing.T) {
	spec := &asyncSpec{
		Info: asyncInfo{
			Title:   "Test API",
			Version: "1.0",
			Desc:    strings.Repeat("x", 850),
		},
		Comps: asyncComponents{Schemas: map[string]asyncSchema{}},
	}
	var buf bytes.Buffer
	renderPage(&buf, spec, "dev")
	assert.Contains(t, buf.String(), "truncated")
}

func TestRenderPage_ProdEnv_RendersBadge(t *testing.T) {
	spec := &asyncSpec{Info: asyncInfo{Title: "Test API", Version: "1.0"}, Comps: asyncComponents{Schemas: map[string]asyncSchema{}}}
	var buf bytes.Buffer
	renderPage(&buf, spec, "production")
	assert.Contains(t, buf.String(), "PRODUCTION")
}

func TestRenderPage_EmptyEnv_DefaultsBadge(t *testing.T) {
	spec := &asyncSpec{Info: asyncInfo{Title: "Test API", Version: "1.0"}, Comps: asyncComponents{Schemas: map[string]asyncSchema{}}}
	var buf bytes.Buffer
	renderPage(&buf, spec, "")
	assert.Contains(t, buf.String(), "DEV")
}

// ── HTTP handlers ─────────────────────────────────────────────────────────────

func newTestGinContext() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/asyncapi", http.NoBody)
	return c, w
}

func TestAsyncAPIHandler_RealSpec_Returns200HTML(t *testing.T) {
	c, w := newTestGinContext()
	c.Set(envLabelContextKey, "dev")

	AsyncAPIHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
	body := w.Body.String()
	assert.Contains(t, body, "TenantOffboarded")
	assert.Contains(t, body, "TenantMembershipRemoved")
	assert.NotContains(t, body, "Published Messages")
}

func TestAsyncAPIYAMLHandler_ReturnsEmbeddedSpecVerbatim(t *testing.T) {
	c, w := newTestGinContext()

	AsyncAPIYAMLHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "application/yaml")
	assert.Contains(t, w.Body.String(), "asyncapi: 3.0.0")
	assert.Contains(t, w.Body.String(), "TenantOffboarded")
}

func TestEnvMiddleware_StashesEnvOnContext(t *testing.T) {
	r := gin.New()
	r.Use(envMiddleware("staging"))
	r.GET("/asyncapi", AsyncAPIHandler)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/asyncapi", http.NoBody)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "STAGING")
}

// ── router wiring ─────────────────────────────────────────────────────────────

func TestRegisterDocsRoutes_Active_MountsAsyncAPIRoutes(t *testing.T) {
	engine := gin.New()
	registerDocsRoutes(engine, DocsConfig{Environment: "development"})

	for _, path := range []string{"/asyncapi", "/asyncapi.yaml"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
		engine.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code, "path=%s", path)
	}
}

func TestRegisterDocsRoutes_Inactive_AsyncAPIRoutesAbsent(t *testing.T) {
	engine := gin.New()
	registerDocsRoutes(engine, DocsConfig{Environment: "production", Enabled: false})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/asyncapi", http.NoBody)
	engine.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestRegisterDocsRoutes_ProductionWithToken_RequiresBearerToken(t *testing.T) {
	engine := gin.New()
	registerDocsRoutes(engine, DocsConfig{Environment: "production", Enabled: true, AuthToken: "secret"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/asyncapi", http.NoBody)
	engine.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/asyncapi", http.NoBody)
	req2.Header.Set("Authorization", "Bearer secret")
	engine.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)
}
