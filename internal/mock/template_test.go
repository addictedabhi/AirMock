package mock

import (
	"net/http/httptest"
	"testing"
)

func testRequestContext(body string) RequestContext {
	return BuildRequestContext(httptest.NewRequest("POST", "/", nil), []byte(body), nil)
}

func TestRenderBodyFieldAccessStillWorksOnJSONObjectBody(t *testing.T) {
	reqCtx := testRequestContext(`{"orderId":"1234","address":{"city":"NYC"}}`)
	got, err := RenderBody(`Order {{.Request.Body.orderId}} in {{.Request.Body.address.city}}`, reqCtx, RenderOptions{})
	if err != nil {
		t.Fatalf("RenderBody: %v", err)
	}
	if got != "Order 1234 in NYC" {
		t.Fatalf("got %q", got)
	}
}

// TestRenderBodyBareObjectRendersAsJSONNotGoMapSyntax guards against a real
// user-visible bug: a template that references {{.Request.Body}} bare
// (rather than drilling into a specific field) used to render Go's default
// map-formatting ("map[orderId:1234]") instead of anything resembling
// JSON — surfaced by an async mock's email callback literally sending
// "Hello, map[orderId:1234]" to a customer.
func TestRenderBodyBareObjectRendersAsJSONNotGoMapSyntax(t *testing.T) {
	reqCtx := testRequestContext(`{"orderId":"1234"}`)
	got, err := RenderBody(`Hello, {{.Request.Body}}`, reqCtx, RenderOptions{})
	if err != nil {
		t.Fatalf("RenderBody: %v", err)
	}
	want := `Hello, {"orderId":"1234"}`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderBodyBareNestedObjectAlsoRendersAsJSON(t *testing.T) {
	reqCtx := testRequestContext(`{"orderId":"1234","address":{"city":"NYC","zip":"10001"}}`)
	got, err := RenderBody(`{{.Request.Body.address}}`, reqCtx, RenderOptions{})
	if err != nil {
		t.Fatalf("RenderBody: %v", err)
	}
	want := `{"city":"NYC","zip":"10001"}`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderBodyBareArrayRendersAsJSON(t *testing.T) {
	reqCtx := testRequestContext(`{"items":["a","b","c"]}`)
	got, err := RenderBody(`{{.Request.Body.items}}`, reqCtx, RenderOptions{})
	if err != nil {
		t.Fatalf("RenderBody: %v", err)
	}
	want := `["a","b","c"]`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderBodyNonJSONStringBodyUnaffected(t *testing.T) {
	reqCtx := testRequestContext(`not json`)
	got, err := RenderBody(`{{.Request.Body}}`, reqCtx, RenderOptions{})
	if err != nil {
		t.Fatalf("RenderBody: %v", err)
	}
	if got != "not json" {
		t.Fatalf("got %q", got)
	}
}
