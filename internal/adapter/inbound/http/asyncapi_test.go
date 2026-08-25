package http

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
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
// TenantMembershipsPurged and MembershipRevoked must come back tagged
// consumed, and there must be zero published messages.
func TestLoadAsyncSpec_RealEmbeddedSpec_BothMessagesConsumed(t *testing.T) {
	spec, err := loadAsyncSpec()
	require.NoError(t, err)
	require.NotNil(t, spec)

	pub, con := splitMessagesByDirection(spec.Comps.Messages)
	assert.Empty(t, pub, "this service publishes zero events (TAC-EVT-1)")
	assert.ElementsMatch(t, []string{"TenantMembershipsPurged", "MembershipRevoked"}, con)
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
	assert.Contains(t, body, "TenantMembershipsPurged")
	assert.Contains(t, body, "MembershipRevoked")
	assert.NotContains(t, body, "Published Messages")
}

func TestAsyncAPIYAMLHandler_ReturnsEmbeddedSpecVerbatim(t *testing.T) {
	c, w := newTestGinContext()

	AsyncAPIYAMLHandler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "application/yaml")
	assert.Contains(t, w.Body.String(), "asyncapi: 3.0.0")
	assert.Contains(t, w.Body.String(), "TenantMembershipsPurged")
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

// ── walkYAML ──────────────────────────────────────────────────────────────────

func TestWalkYAML_NilNode_ReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", walkYAML(nil, "key"))
}

func TestWalkYAML_EmptyKeys_ReturnsEmpty(t *testing.T) {
	node := &yaml.Node{Kind: yaml.MappingNode}
	assert.Equal(t, "", walkYAML(node))
}

func TestWalkYAML_DocumentNode_UnwrapsAndFinds(t *testing.T) {
	inner := &yaml.Node{
		Kind: yaml.MappingNode,
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "foo"},
			{Kind: yaml.ScalarNode, Value: "bar"},
		},
	}
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{inner}}
	assert.Equal(t, "bar", walkYAML(doc, "foo"))
}

func TestWalkYAML_MappingNode_KeyFound_ReturnsValue(t *testing.T) {
	node := &yaml.Node{
		Kind: yaml.MappingNode,
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "mykey"},
			{Kind: yaml.ScalarNode, Value: "myval"},
		},
	}
	assert.Equal(t, "myval", walkYAML(node, "mykey"))
}

func TestWalkYAML_MappingNode_KeyNotFound_ReturnsEmpty(t *testing.T) {
	node := &yaml.Node{
		Kind:    yaml.MappingNode,
		Content: []*yaml.Node{},
	}
	assert.Equal(t, "", walkYAML(node, "missing"))
}

func TestWalkYAML_NestedPath_ReturnsDeepValue(t *testing.T) {
	child := &yaml.Node{
		Kind: yaml.MappingNode,
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "value"},
			{Kind: yaml.ScalarNode, Value: "deep"},
		},
	}
	root := &yaml.Node{
		Kind: yaml.MappingNode,
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "parent"},
			child,
		},
	}
	assert.Equal(t, "deep", walkYAML(root, "parent", "value"))
}

// ── snsEventType ──────────────────────────────────────────────────────────────

func TestSnsEventType_NilNode_ReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", snsEventType(nil))
}

func TestSnsEventType_ZeroKindNode_ReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", snsEventType(&yaml.Node{Kind: 0}))
}

func TestSnsEventType_EventTypeCamelCase_ReturnsValue(t *testing.T) {
	node := mustParseYAML(t, `
sns:
  messageAttributes:
    EventType:
      value: MyEvent
`)
	assert.Equal(t, "MyEvent", snsEventType(node))
}

func TestSnsEventType_EventTypeSnakeCase_ReturnsValue(t *testing.T) {
	node := mustParseYAML(t, `
sns:
  messageAttributes:
    event_type:
      value: my_event
`)
	assert.Equal(t, "my_event", snsEventType(node))
}

func mustParseYAML(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(src), &doc))
	return &doc
}

// ── propType ──────────────────────────────────────────────────────────────────

func TestPropType_Ref_ReturnsSchemaName(t *testing.T) {
	p := &asyncProp{Ref: "#/components/schemas/MyType"}
	assert.Equal(t, "MyType", propType(p))
}

func TestPropType_Format_IncludesFormat(t *testing.T) {
	p := &asyncProp{Type: "string", Format: "uuid"}
	assert.Equal(t, "string(uuid)", propType(p))
}

func TestPropType_ArrayWithItemType_ReturnsArrayType(t *testing.T) {
	p := &asyncProp{Type: "array", Items: &asyncProp{Type: "string"}}
	assert.Equal(t, "array<string>", propType(p))
}

func TestPropType_ArrayWithItemRef_ReturnsArrayRef(t *testing.T) {
	p := &asyncProp{Type: "array", Items: &asyncProp{Ref: "#/components/schemas/Entry"}}
	assert.Equal(t, "array<Entry>", propType(p))
}

func TestPropType_PlainType_ReturnsType(t *testing.T) {
	p := &asyncProp{Type: "boolean"}
	assert.Equal(t, "boolean", propType(p))
}

// ── typeHTML ──────────────────────────────────────────────────────────────────

func TestTypeHTML_Ref_RendersSchemaLink(t *testing.T) {
	p := &asyncProp{Ref: "#/components/schemas/MySchema"}
	out := typeHTML(p)
	assert.Contains(t, out, `href="#schema-MySchema"`)
	assert.Contains(t, out, "MySchema")
}

func TestTypeHTML_ArrayWithRef_RendersArrayLink(t *testing.T) {
	p := &asyncProp{Type: "array", Items: &asyncProp{Ref: "#/components/schemas/Item"}}
	out := typeHTML(p)
	assert.Contains(t, out, "array")
	assert.Contains(t, out, `href="#schema-Item"`)
}

func TestTypeHTML_Fallback_RendersPropType(t *testing.T) {
	p := &asyncProp{Type: "integer"}
	out := typeHTML(p)
	assert.Contains(t, out, "integer")
	assert.Contains(t, out, `class="prop-type"`)
}

// ── resolveSchema ─────────────────────────────────────────────────────────────

func TestResolveSchema_Direct_NoAllOfNoRef_ReturnsSelf(t *testing.T) {
	sc := &asyncSchema{Properties: map[string]asyncProp{"id": {Type: "string"}}}
	comps := &asyncComponents{Schemas: map[string]asyncSchema{}}
	result := resolveSchema(sc, comps)
	assert.Same(t, sc, result)
}

func TestResolveSchema_Ref_Found_ReturnsReferenced(t *testing.T) {
	comps := &asyncComponents{
		Schemas: map[string]asyncSchema{
			"MyRef": {Properties: map[string]asyncProp{"name": {Type: "string"}}},
		},
	}
	sc := &asyncSchema{Ref: "#/components/schemas/MyRef"}
	result := resolveSchema(sc, comps)
	_, ok := result.Properties["name"]
	assert.True(t, ok)
}

func TestResolveSchema_Ref_NotFound_ReturnsSelf(t *testing.T) {
	comps := &asyncComponents{Schemas: map[string]asyncSchema{}}
	sc := &asyncSchema{Ref: "#/components/schemas/Missing"}
	result := resolveSchema(sc, comps)
	assert.Same(t, sc, result)
}

func TestResolveSchema_AllOf_MergesProperties(t *testing.T) {
	comps := &asyncComponents{Schemas: map[string]asyncSchema{}}
	sc := &asyncSchema{
		AllOf: []asyncSchema{
			{Properties: map[string]asyncProp{"a": {Type: "string"}}, Required: []string{"a"}},
			{Properties: map[string]asyncProp{"b": {Type: "integer"}}},
		},
	}
	result := resolveSchema(sc, comps)
	_, hasA := result.Properties["a"]
	_, hasB := result.Properties["b"]
	assert.True(t, hasA)
	assert.True(t, hasB)
	assert.Contains(t, result.Required, "a")
}

// ── renderPropsTable ──────────────────────────────────────────────────────────

func TestRenderPropsTable_NilSchema_WritesNoPropsMessage(t *testing.T) {
	var buf bytes.Buffer
	renderPropsTable(&buf, nil, "test")
	assert.Contains(t, buf.String(), "No properties")
}

func TestRenderPropsTable_EmptyProperties_WritesNoPropsMessage(t *testing.T) {
	var buf bytes.Buffer
	renderPropsTable(&buf, &asyncSchema{Properties: map[string]asyncProp{}}, "test")
	assert.Contains(t, buf.String(), "No properties")
}

func TestRenderPropsTable_WithEnum_RendersEnumValues(t *testing.T) {
	var buf bytes.Buffer
	sc := &asyncSchema{
		Properties: map[string]asyncProp{
			"status": {Type: "string", Enum: []string{"active", "suspended"}},
		},
	}
	renderPropsTable(&buf, sc, "test")
	out := buf.String()
	assert.Contains(t, out, "active")
	assert.Contains(t, out, "suspended")
}

func TestRenderPropsTable_WithItemEnum_RendersItemEnumValues(t *testing.T) {
	var buf bytes.Buffer
	sc := &asyncSchema{
		Properties: map[string]asyncProp{
			"roles": {Type: "array", Items: &asyncProp{Enum: []string{"admin", "member"}}},
		},
	}
	renderPropsTable(&buf, sc, "test")
	out := buf.String()
	assert.Contains(t, out, "admin")
	assert.Contains(t, out, "member")
}

func TestRenderPropsTable_WithExample_RendersExampleBlock(t *testing.T) {
	var buf bytes.Buffer
	sc := &asyncSchema{
		Properties: map[string]asyncProp{
			"id": {Type: "string", Example: "some-uuid"},
		},
	}
	renderPropsTable(&buf, sc, "test")
	assert.Contains(t, buf.String(), "some-uuid")
}

// ── splitMessagesByDirection (published branch) ───────────────────────────────

func TestSplitMessagesByDirection_AllPublished_ConsumedEmpty(t *testing.T) {
	pub, con := splitMessagesByDirection(map[string]asyncMessage{
		"EventA": {Tags: []asyncRef{{Ref: "#/components/tags/published"}}},
		"EventB": {Tags: []asyncRef{{Ref: "#/components/tags/published"}}},
	})
	assert.ElementsMatch(t, []string{"EventA", "EventB"}, pub)
	assert.Empty(t, con)
}

func TestSplitMessagesByDirection_Mixed_SplitsCorrectly(t *testing.T) {
	pub, con := splitMessagesByDirection(map[string]asyncMessage{
		"Published": {Tags: []asyncRef{{Ref: "#/components/tags/published"}}},
		"Consumed":  {Tags: []asyncRef{{Ref: "#/components/tags/consumed"}}},
	})
	assert.Equal(t, []string{"Published"}, pub)
	assert.Equal(t, []string{"Consumed"}, con)
}

// ── UnmarshalYAML decode error ────────────────────────────────────────────────

func TestUnmarshalYAML_DecodeError_ReturnsError(t *testing.T) {
	// A ScalarNode (not a MappingNode) will fail to decode into asyncSchema struct.
	node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	var sc asyncSchema
	err := sc.UnmarshalYAML(node)
	require.Error(t, err)
}

// ── envLabelFromGin non-string value ─────────────────────────────────────────

func TestEnvLabelFromGin_NonStringValue_ReturnsEmpty(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	// Set the context key to a non-string value → type assertion fails → returns ""
	c.Set(envLabelContextKey, 12345)
	assert.Equal(t, "", envLabelFromGin(c))
}

// ── renderPage with published messages ───────────────────────────────────────

func TestRenderPage_PublishedMessages_RendersBothSections(t *testing.T) {
	spec := &asyncSpec{
		Info: asyncInfo{Title: "Test API", Version: "1.0"},
		Comps: asyncComponents{
			Messages: map[string]asyncMessage{
				"SomePublishedEvent": {Title: "Some Published", Tags: []asyncRef{{Ref: "#/components/tags/published"}}},
				"SomeConsumedEvent":  {Title: "Some Consumed", Tags: []asyncRef{{Ref: "#/components/tags/consumed"}}},
			},
			Schemas: map[string]asyncSchema{},
		},
	}
	var buf bytes.Buffer
	renderPage(&buf, spec, "dev")
	out := buf.String()
	assert.Contains(t, out, "Published Messages")
	assert.Contains(t, out, "Consumed Messages")
	assert.Contains(t, out, `id="msg-SomePublishedEvent"`)
	assert.Contains(t, out, `id="msg-SomeConsumedEvent"`)
}

// ── renderMessage empty title + snsEventType ──────────────────────────────────

func TestRenderMessage_EmptyTitle_UsesName(t *testing.T) {
	var buf bytes.Buffer
	// msg.Title is empty → title falls back to the name parameter
	msg := &asyncMessage{Title: "", Tags: []asyncRef{{Ref: "#/components/tags/consumed"}}}
	renderMessage(&buf, "MyFallbackName", msg, &asyncComponents{Schemas: map[string]asyncSchema{}})
	assert.Contains(t, buf.String(), "MyFallbackName")
}

func TestRenderMessage_WithSNSEventType_RendersAttributeSpan(t *testing.T) {
	// Build a bindings node that contains an EventType attribute.
	bindingsNode := mustParseYAML(t, `
sns:
  messageAttributes:
    EventType:
      value: OrderCreated
`)
	var buf bytes.Buffer
	msg := &asyncMessage{
		Title:    "Order Created",
		Bindings: *bindingsNode,
		Tags:     []asyncRef{{Ref: "#/components/tags/consumed"}},
	}
	renderMessage(&buf, "OrderCreated", msg, &asyncComponents{Schemas: map[string]asyncSchema{}})
	assert.Contains(t, buf.String(), "OrderCreated")
	assert.Contains(t, buf.String(), "event_type")
}

// ── resolveSchema: duplicate allOf properties (dedup path) ────────────────────

func TestResolveSchema_AllOf_DuplicatePropertyOrder_Deduped(t *testing.T) {
	comps := &asyncComponents{Schemas: map[string]asyncSchema{}}
	sc := &asyncSchema{
		AllOf: []asyncSchema{
			{
				Properties:    map[string]asyncProp{"shared": {Type: "string"}, "a": {Type: "integer"}},
				PropertyOrder: []string{"shared", "a"},
			},
			{
				Properties:    map[string]asyncProp{"shared": {Type: "string"}, "b": {Type: "boolean"}},
				PropertyOrder: []string{"shared", "b"},
			},
		},
	}
	result := resolveSchema(sc, comps)
	// "shared" appears in both — must appear only once in merged PropertyOrder
	count := 0
	for _, k := range result.PropertyOrder {
		if k == "shared" {
			count++
		}
	}
	assert.Equal(t, 1, count, "duplicate key must be deduped in merged PropertyOrder")
}

// ── toACLResponses with entries ───────────────────────────────────────────────

func TestHandler_List_WithEntries_ResponseContainsEntries(t *testing.T) {
	entryID := uuid.New()
	repo := emptyRepo()
	repo.listFn = func(context.Context, uuid.UUID, uuid.UUID) ([]domain.TenderACLEntry, error) {
		return []domain.TenderACLEntry{{ID: entryID, AccessLevel: domain.ACLView}}, nil
	}
	h := newTestHandler(repo, &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodGet, "/", "", tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String())
	h.List(c)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), entryID.String())
}

// ── Grant handler with reason field ──────────────────────────────────────────

func TestHandler_Grant_WithReason_Returns201(t *testing.T) {
	h := newTestHandler(emptyRepo(), activeChecker(uuid.New()), &fakeCache{}, t)
	tenant := uuid.New()
	body := `{"user_id":"` + uuid.New().String() + `","access_level":"view","reason":"test reason"}`
	c, w := buildCtx(http.MethodPost, "/", body, tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String())
	h.Grant(c)
	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Contains(t, w.Body.String(), "test reason")
}

// ── renderPropsTable: property in map missing from order slice (guard path) ───

func TestRenderPropsTable_PropertyNotInOrder_StillRendered(t *testing.T) {
	var buf bytes.Buffer
	sc := &asyncSchema{
		// PropertyOrder omits "extra" — the guard must catch it and append
		PropertyOrder: []string{"name"},
		Properties: map[string]asyncProp{
			"name":  {Type: "string"},
			"extra": {Type: "integer"},
		},
	}
	renderPropsTable(&buf, sc, "test")
	out := buf.String()
	assert.Contains(t, out, "name")
	assert.Contains(t, out, "extra")
}
